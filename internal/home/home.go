package home

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/oidc"
)

// Home's cookies are __Host- prefixed: Secure, Path=/, no Domain, so no app of the household, same-site with Home, can
// set or shadow them (#83).
const (
	sessionCookie = "__Host-homerslab-home"
	signInCookie  = "__Host-homerslab-home-sign-in"
	// signedOutCookie marks a browser that signed out of Home: its next sign-in is a fresh one, so the household
	// account's session there never opens the last person's Home for whoever holds the device next.
	signedOutCookie = "__Host-homerslab-home-signed-out"
	signedOutLife   = 400 * 24 * time.Hour
	// SessionLife is how long a session on Home lasts. After it, Home sends the person to the sign-in again, which
	// lets them straight back while their session there lasts.
	SessionLife = 12 * time.Hour
	// attemptLife is how long a sign-in may take, from Home to the sign-in and back.
	attemptLife = 10 * time.Minute
	// maxSessions bounds the sessions Home keeps in memory. Beyond it, a new one is refused rather than ending a live
	// one. Only a person the sign-in let in makes one.
	maxSessions = 4096
	// discoveryLife is how long Home keeps the sign-in's discovery document; a failure to read it, discoveryRetry.
	discoveryLife  = 10 * time.Minute
	discoveryRetry = 5 * time.Second
)

// Groups are the household account's groups of Home's roles (idp.RoleGroup; a test holds them equal, as Home is built
// from nothing of idp's).
var Groups = map[string]string{"admin": "homerslab-admins", "adult": "homerslab-adults", "kid": "homerslab-kids"}

// roles are Home's roles, from the most to the least a person may do.
var roles = []string{"admin", "adult", "kid"}

// Config is how Home is wired.
type Config struct {
	// Projection is the projection's file, which the manager writes.
	Projection string
	// ClientSecret is the file that holds Home's client secret: a credential of Home's unit, which systemd decrypts
	// into the unit's own credentials directory.
	ClientSecret string
	// Web is the built pages.
	Web fs.FS
	Now func() time.Time
	Log *slog.Logger
}

// Home is a running Home. Its sessions live in memory only: a Home that restarts sends people to the sign-in again.
type Home struct {
	cfg Config
	// seal seals a sign-in under way into the browser's own cookie, with a key that lives in Home's memory only: anyone
	// may start sign-ins, as many as they like, and none of it takes a place a person's sign-in needs.
	seal     cipher.AEAD
	mu       sync.Mutex
	sessions map[string]session
	read     struct {
		p  *Projection
		fi os.FileInfo
	}
	// discovery is the sign-in's discovery document as Home last read it, for the client it read it with.
	discovery struct {
		sync.Mutex
		client string
		e      oidc.Endpoints
		err    error
		until  time.Time
	}
}

// attempt is a sign-in under way, as its cookie carries it, sealed.
type attempt struct {
	oidc.Attempt
	Expires time.Time `json:"expires"`
}

// session is a signed-in person, as the sign-in named them.
type session struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	expires  time.Time
}

// New makes a Home.
func New(c Config) *Home {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	b, _ := aes.NewCipher(k) // a 32-byte key: AES-256
	seal, _ := cipher.NewGCM(b)
	return &Home{cfg: c, seal: seal, sessions: map[string]session{}}
}

// sealAttempt is a's cookie value: sealed and authenticated with Home's key, bound to the sign-in cookie's name.
func (h *Home) sealAttempt(a attempt) string {
	plain, _ := json.Marshal(a)
	nonce := make([]byte, h.seal.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.RawURLEncoding.EncodeToString(h.seal.Seal(nonce, nonce, plain, []byte(signInCookie)))
}

// openAttempt is the sign-in under way that v carries, if Home sealed it.
func (h *Home) openAttempt(v string) (attempt, bool) {
	var a attempt
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil || len(b) < h.seal.NonceSize() {
		return a, false
	}
	n := h.seal.NonceSize()
	plain, err := h.seal.Open(nil, b[:n], b[n:], []byte(signInCookie))
	if err != nil || json.Unmarshal(plain, &a) != nil {
		return attempt{}, false
	}
	return a, true
}

// discover is the sign-in's discovery document, read again at most every discoveryLife (and after a failure, at
// most every discoveryRetry), or when the projection names another client.
func (h *Home) discover(ctx context.Context, c oidc.Client, p Projection) (oidc.Endpoints, error) {
	d := &h.discovery
	d.Lock()
	defer d.Unlock()
	who, now := c.Issuer+"\x00"+c.ID+"\x00"+p.SignIn.Upstream, h.cfg.Now()
	if d.client == who && now.Before(d.until) {
		return d.e, d.err
	}
	d.client = who
	d.e, d.err = c.Discover(context.WithoutCancel(ctx)) // one person leaving doesn't fail the others waiting
	if d.err != nil {
		d.until = now.Add(discoveryRetry)
	} else {
		d.until = now.Add(discoveryLife)
	}
	return d.e, d.err
}

type ctxKey int

const (
	ctxProjection ctxKey = iota
	ctxSession
)

// Handler is Home, as the edge reaches it on Home's socket.
func (h *Home) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/sign-in", h.signInStart)
	mux.HandleFunc("GET /auth/callback", h.signInDone)
	mux.HandleFunc("POST /auth/sign-out", h.signOut)
	mux.Handle("GET /{$}", h.signedIn(http.HandlerFunc(h.page)))
	mux.Handle("GET /api/v1/home", h.signedIn(http.HandlerFunc(h.getHome)))
	mux.Handle("GET /api/v1/apps/{id}/icon", h.signedIn(http.HandlerFunc(h.getIcon)))
	// The manifest and the logo's icons need no session: a browser fetches a manifest without cookies.
	mux.HandleFunc("GET /manifest.webmanifest", h.file("home/manifest.webmanifest"))
	mux.HandleFunc("GET /home/{file}", func(w http.ResponseWriter, r *http.Request) { h.file("home/"+r.PathValue("file"))(w, r) })
	mux.HandleFunc("GET /assets/{file}", func(w http.ResponseWriter, r *http.Request) { h.file("assets/"+r.PathValue("file"))(w, r) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fail(w, http.StatusNotFound, "not-found") })
	return securityHeaders(h.ownName(sameOrigin(mux)))
}

// securityHeaders are every surface's: nothing frames Home, nothing is sniffed or cached, no Referer leaves it.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; style-src-attr 'unsafe-inline'; "+
			"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		hd.Set("Cross-Origin-Resource-Policy", "same-origin")
		hd.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ownName answers only Home's own name, from the projection, whatever the edge forwards; before the manager has
// written the projection, Home answers nothing but that it isn't ready.
func (h *Home) ownName(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := h.projection()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				h.cfg.Log.Error("reading the projection", "err", err)
			}
			fail(w, http.StatusServiceUnavailable, "not-ready")
			return
		}
		own, err := url.Parse(p.Home)
		host := r.Host
		if hh, _, err := net.SplitHostPort(r.Host); err == nil {
			host = hh
		}
		if err != nil || own.Hostname() == "" || !strings.EqualFold(host, own.Hostname()) {
			fail(w, http.StatusMisdirectedRequest, "wrong-host")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxProjection, p)))
	})
}

// projection is the projection as the manager last wrote it: read again only when its file changed (the manager
// replaces it whole, by a rename, so a new file is a new inode).
func (h *Home) projection() (Projection, error) {
	fi, err := os.Stat(h.cfg.Projection)
	if err != nil {
		return Projection{}, err
	}
	h.mu.Lock()
	if h.read.p != nil && os.SameFile(fi, h.read.fi) && fi.ModTime().Equal(h.read.fi.ModTime()) && fi.Size() == h.read.fi.Size() {
		p := *h.read.p
		h.mu.Unlock()
		return p, nil
	}
	h.mu.Unlock()
	p, err := Read(h.cfg.Projection)
	if err != nil {
		return Projection{}, err
	}
	h.mu.Lock()
	h.read.p, h.read.fi = &p, fi
	h.mu.Unlock()
	return p, nil
}

func projection(r *http.Request) Projection { return r.Context().Value(ctxProjection).(Projection) }

// sameOrigin refuses cross-site changes (#83): every app of the household is same-site with Home, so a change must
// come from Home's own page: Sec-Fetch-Site: same-origin, Home's exact Origin, and JSON, which no form can send.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") != "same-origin" || r.Header.Get("Origin") != projection(r).Home {
			fail(w, http.StatusForbidden, "cross-origin")
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			fail(w, http.StatusUnsupportedMediaType, "not-json")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// key is what Home keeps of a cookie's value: its hash, never the value itself.
func key(v string) string {
	s := sha256.Sum256([]byte(v))
	return string(s[:])
}

func setCookie(w http.ResponseWriter, name, value string, life time.Duration) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if life > 0 {
		c.MaxAge = int(life / time.Second)
	} else {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// client is Home's client of the sign-in, reaching its server at the upstream the projection names.
func (h *Home) client(p Projection) (oidc.Client, error) {
	if p.SignIn == nil || p.SignIn.Issuer == "" || p.SignIn.ClientID == "" {
		return oidc.Client{}, errors.New("the household account isn't set up yet")
	}
	secret, err := os.ReadFile(h.cfg.ClientSecret)
	if err != nil {
		return oidc.Client{}, err
	}
	iss, err := url.Parse(p.SignIn.Issuer)
	if err != nil || iss.Scheme != "https" {
		return oidc.Client{}, errors.New("the projection's issuer isn't an https URL")
	}
	return oidc.Client{Issuer: p.SignIn.Issuer, ID: p.SignIn.ClientID, Secret: mesh.Secret(strings.TrimSpace(string(secret))),
		Redirect: p.Home + "/auth/callback",
		HTTP:     &http.Client{Timeout: 15 * time.Second, Transport: oidc.Through(iss.Scheme+"://"+iss.Host, p.SignIn.Upstream)}}, nil
}

// signInStart sends the browser to the sign-in, with a new attempt that the browser carries itself, sealed in a cookie:
// Home keeps nothing of it. Unlike Admin's, it isn't a fresh sign-in: a person signed in at the sign-in already comes
// straight back.
func (h *Home) signInStart(w http.ResponseWriter, r *http.Request) {
	c, err := h.client(projection(r))
	if err != nil {
		h.cfg.Log.Warn("Home's client of the sign-in", "err", err)
		fail(w, http.StatusServiceUnavailable, "no-sign-in-yet")
		return
	}
	e, err := h.discover(r.Context(), c, projection(r))
	if err != nil {
		h.cfg.Log.Warn("the sign-in's discovery document", "err", err)
		fail(w, http.StatusBadGateway, "sign-in-unreachable")
		return
	}
	a, now := oidc.NewAttempt(), h.cfg.Now()
	if ck, err := r.Cookie(signedOutCookie); err == nil && ck.Value != "" {
		a = oidc.FreshAttempt(now)
	}
	setCookie(w, signInCookie, h.sealAttempt(attempt{Attempt: a, Expires: now.Add(attemptLife)}), attemptLife)
	http.Redirect(w, r, c.AuthorizeURL(e, a), http.StatusSeeOther)
}

// signInDone takes the browser back from the sign-in: the attempt must be this browser's, the code good, the ID token
// verified, and the person in one of the household's roles. Then Home opens a session. A return comes once: the cookie
// goes with it, and the sign-in takes a code only once, for this attempt's PKCE verifier.
func (h *Home) signInDone(w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie(signInCookie)
	setCookie(w, signInCookie, "", 0)
	if err != nil {
		fail(w, http.StatusForbidden, "no-sign-in-attempt")
		return
	}
	now := h.cfg.Now()
	a, ok := h.openAttempt(ck.Value)
	if !ok || !now.Before(a.Expires) {
		fail(w, http.StatusForbidden, "no-sign-in-attempt")
		return
	}
	c, err := h.client(projection(r))
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "no-sign-in-yet")
		return
	}
	e, err := h.discover(r.Context(), c, projection(r))
	if err != nil {
		fail(w, http.StatusBadGateway, "sign-in-unreachable")
		return
	}
	q := r.URL.Query()
	cl, err := c.Finish(r.Context(), e, a.Attempt, q.Get("state"), q.Get("code"), now)
	if err != nil {
		h.cfg.Log.Warn("refused a sign-in to Home", "err", err)
		fail(w, http.StatusForbidden, "sign-in-refused")
		return
	}
	s := session{Username: cl.Username, Name: cl.Name, expires: now.Add(SessionLife)}
	for _, ro := range roles {
		if slices.Contains(cl.Groups, Groups[ro]) {
			s.Role = ro
			break
		}
	}
	if s.Role == "" || s.Username == "" {
		h.cfg.Log.Warn("refused a sign-in to Home: in none of the household's roles", "username", cl.Username)
		fail(w, http.StatusForbidden, "not-in-the-household")
		return
	}
	if s.Name == "" {
		s.Name = s.Username
	}
	tok := random()
	h.mu.Lock()
	for k, v := range h.sessions {
		if now.After(v.expires) {
			delete(h.sessions, k)
		}
	}
	full := len(h.sessions) >= maxSessions
	if !full {
		h.sessions[key(tok)] = s
	}
	h.mu.Unlock()
	if full {
		fail(w, http.StatusTooManyRequests, "too-many-sessions")
		return
	}
	setCookie(w, sessionCookie, tok, SessionLife)
	setCookie(w, signedOutCookie, "", 0)
	h.cfg.Log.Info("signed in to Home", "username", s.Username, "role", s.Role)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// signOut ends the browser's session.
func (h *Home) signOut(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		h.mu.Lock()
		delete(h.sessions, key(ck.Value))
		h.mu.Unlock()
	}
	setCookie(w, sessionCookie, "", 0)
	setCookie(w, signedOutCookie, "1", signedOutLife)
	w.WriteHeader(http.StatusNoContent)
}

// signedIn wants a live session; without one, the page goes to the sign-in and an API call is refused.
func (h *Home) signedIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var s session
		ok := false
		if ck, err := r.Cookie(sessionCookie); err == nil && ck.Value != "" {
			h.mu.Lock()
			s, ok = h.sessions[key(ck.Value)]
			if ok && !h.cfg.Now().Before(s.expires) {
				delete(h.sessions, key(ck.Value))
				ok = false
			}
			h.mu.Unlock()
		}
		if !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				fail(w, http.StatusUnauthorized, "signed-out")
				return
			}
			http.Redirect(w, r, "/auth/sign-in", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxSession, s)))
	})
}

func signedInAs(r *http.Request) session { return r.Context().Value(ctxSession).(session) }

// mine are the apps whose audience has the person's role.
func mine(p Projection, s session) []App {
	var out []App
	for _, a := range p.Apps {
		if slices.Contains(a.Audience, s.Role) {
			out = append(out, a)
		}
	}
	return out
}

// tile is an app as one person's Home shows it.
type tile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Purpose  string `json:"purpose"`
	Category string `json:"category"`
	URL      string `json:"url"`
	Health   string `json:"health"`
	Icon     string `json:"icon,omitempty"`
}

// getHome is what the person's Home shows: who they are, and the apps they may open, each with its health.
func (h *Home) getHome(w http.ResponseWriter, r *http.Request) {
	p, s := projection(r), signedInAs(r)
	out := struct {
		Household string  `json:"household"`
		Person    session `json:"person"`
		Admin     string  `json:"admin,omitempty"`
		Apps      []tile  `json:"apps"`
	}{Household: p.Household, Person: s, Apps: []tile{}}
	if s.Role == "admin" {
		out.Admin = p.Admin
	}
	for _, a := range mine(p, s) {
		t := tile{ID: a.ID, Name: a.Name, Purpose: a.Purpose, Category: a.Category, URL: a.URL, Health: a.Health}
		if a.Icon != nil {
			t.Icon = "/api/v1/apps/" + url.PathEscape(a.ID) + "/icon"
		}
		out.Apps = append(out.Apps, t)
	}
	writeJSON(w, http.StatusOK, out)
}

var iconTypes = []string{"image/svg+xml", "image/png", "image/jpeg", "image/webp"}

// getIcon is an app's icon, for a person who may open the app, as a picture only: an SVG opened on its own runs no
// script and loads nothing.
func (h *Home) getIcon(w http.ResponseWriter, r *http.Request) {
	for _, a := range mine(projection(r), signedInAs(r)) {
		if a.ID == r.PathValue("id") && a.Icon != nil && slices.Contains(iconTypes, a.Icon.Type) {
			hd := w.Header()
			hd.Set("Content-Type", a.Icon.Type)
			hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
			hd.Set("Cache-Control", "private, max-age=300")
			_, _ = w.Write(a.Icon.Data)
			return
		}
	}
	fail(w, http.StatusNotFound, "no-such-app")
}

// fileTypes are the built files Home serves, by extension.
var fileTypes = map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".png": "image/png",
	".svg": "image/svg+xml", ".ico": "image/x-icon", ".webmanifest": "application/manifest+json", ".woff2": "font/woff2"}

// file serves one built file, by its name in the built pages.
func (h *Home) file(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		typ := fileTypes[path.Ext(name)]
		if typ == "" || !fs.ValidPath(name) || h.cfg.Web == nil {
			fail(w, http.StatusNotFound, "not-found")
			return
		}
		b, err := fs.ReadFile(h.cfg.Web, name)
		if err != nil {
			fail(w, http.StatusNotFound, "not-found")
			return
		}
		w.Header().Set("Content-Type", typ)
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // named by their content's hash
		}
		_, _ = w.Write(b)
	}
}

// page is Home's page, from the built web app. It carries no data: the page asks the API.
func (h *Home) page(w http.ResponseWriter, _ *http.Request) {
	b, err := fs.ReadFile(h.cfg.Web, "home.html")
	if err != nil {
		fail(w, http.StatusNotImplemented, "not-built")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
