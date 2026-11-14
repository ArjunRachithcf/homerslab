// Package catalog is the app definition's schema and its loader. A catalog is a folder: catalog.yaml, and one folder
// per app under apps/, holding the app's manifest (app.yaml), its plain compose file (compose.yaml; none for a host
// app), its icon and its screenshots (spec: The app definition; ADR 0009).
//
// The loader checks the schema: every field known, every required one there, every file it names present. The floor
// (spec: The floor) and catalogs' signatures are checked by their own slices.
package catalog

// Catalog is one catalog's apps.
type Catalog struct {
	ID         string     `yaml:"id" json:"id"`
	Name       string     `yaml:"name" json:"name"`
	Publisher  string     `yaml:"publisher" json:"publisher"`
	Categories []Category `yaml:"categories" json:"categories"`
	Apps       []App      `yaml:"-" json:"-"`
}

// Category is what apps are for, as the Store groups them.
type Category struct {
	ID   string `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
}

// App is one app definition: its manifest, and its compose file. JSON is the manifest as the API shows it.
type App struct {
	// Identity.
	ID          string       `yaml:"id" json:"id"`
	Name        string       `yaml:"name" json:"name"`       // the product's name: Immich
	Purpose     string       `yaml:"purpose" json:"purpose"` // what it is for, beside its name: Photos
	Description string       `yaml:"description" json:"description"`
	Category    string       `yaml:"category" json:"category"`
	Tier        Tier         `yaml:"tier" json:"tier"`
	Icon        string       `yaml:"icon" json:"icon"` // a file in the app's folder
	Screenshots []Screenshot `yaml:"screenshots" json:"screenshots"`

	Versions    Versions     `yaml:"versions" json:"versions"`
	SignIn      []SignIn     `yaml:"signIn" json:"signIn"` // in order of preference
	Names       Names        `yaml:"names" json:"names"`
	Audience    Audience     `yaml:"audience" json:"audience"`
	Data        []Data       `yaml:"data" json:"data"`
	Secrets     []Secret     `yaml:"secrets" json:"secrets"`
	Health      []Health     `yaml:"health" json:"health"`
	Resources   Resources    `yaml:"resources" json:"resources"`
	Extras      []Extra      `yaml:"extras" json:"extras"`
	Host        *HostApp     `yaml:"host" json:"host,omitempty"` // a host app's recipe; nil for an app in containers
	UpdateNotes []UpdateNote `yaml:"updateNotes" json:"updateNotes"`

	// Compose is the app's plain compose file; nil for a host app.
	Compose []byte `yaml:"-" json:"-"`
	// Digest is a SHA-256 of what the definition is made of: its catalog's id and catalog.yaml, its app.yaml and its
	// compose file ("sha256:<hex>").
	Digest string `yaml:"-" json:"digest"`
}

// Tier is how deeply an app is integrated (ADR 0009).
type Tier string

const (
	Verified  Tier = "verified"
	Community Tier = "community"
)

// Screenshot is a picture of the app and what it shows.
type Screenshot struct {
	File    string `yaml:"file" json:"file"`
	Caption string `yaml:"caption" json:"caption"`
}

// Versions are the app's version and its images, pinned.
type Versions struct {
	App    string  `yaml:"app" json:"app"`
	Images []Image `yaml:"images" json:"images"`
}

// Image is the image one compose service runs: the compose file names it by its index digest, and Digests are the
// manifests that index gives each engine on each architecture.
type Image struct {
	Service string                     `yaml:"service" json:"service"`
	Digests map[Engine]map[Arch]string `yaml:"digests" json:"digests"`
}

// Engine is a container engine; Arch an architecture, as node facts name it.
type (
	Engine string
	Arch   string
)

const (
	Docker Engine = "docker"
	Podman Engine = "podman"
	X86_64 Arch   = "x86_64"
	ARM64  Arch   = "arm64"
)

// SignIn is one way the app takes the household account, with what the identity provider needs for it.
type SignIn struct {
	Method SignInMethod `yaml:"method" json:"method"`
	// OIDC: where the provider may send people back, as paths on the app's name or full addresses of its own
	// apps (app.immich:///oauth-callback), and the scopes it asks for.
	RedirectURIs []string `yaml:"redirectUris" json:"redirectUris,omitempty"`
	Scopes       []string `yaml:"scopes" json:"scopes,omitempty"`
	// OIDC: how the app takes its client, as environment variables of its upstream's service (Names.Service), which the
	// agent sets; and the container user that reads the client's secret, a file HomersLab hands it (ClientSecretFile:
	// never the secret itself in the environment). Without Env, HomersLab can't hand the app its client yet.
	Env       *OIDCEnv `yaml:"env" json:"env,omitempty"`
	SecretUID *int     `yaml:"secretUid" json:"secretUid,omitempty"`
	// RefusePaths are paths of the app's own that sign people in some other way (a password the app made itself, a
	// default admin's), which the edge refuses before the app sees them: the household account is the only way in.
	RefusePaths []string `yaml:"refusePaths" json:"refusePaths,omitempty"`
	// LDAP: the attributes it reads.
	Attributes []string `yaml:"attributes" json:"attributes,omitempty"`
}

// OIDCEnv names the environment variables an app takes its OIDC client from; each is optional but the client's id,
// its secret's file, and its issuer or discovery document. None of them carries a secret.
type OIDCEnv struct {
	Issuer    string `yaml:"issuer" json:"issuer,omitempty"`       // the issuer's URL
	Discovery string `yaml:"discovery" json:"discovery,omitempty"` // the issuer's discovery document's URL
	ClientID  string `yaml:"clientId" json:"clientId"`
	// ClientSecretFile takes the path of the file that holds the client's secret.
	ClientSecretFile string `yaml:"clientSecretFile" json:"clientSecretFile"`
	// RedirectURI takes the client's first redirect address, on the app's URL; URL the app's URL.
	RedirectURI string `yaml:"redirectUri" json:"redirectUri,omitempty"`
	URL         string `yaml:"url" json:"url,omitempty"`
	// AdminGroup takes the name of the admins' group, for an app that makes them its own admins.
	AdminGroup string `yaml:"adminGroup" json:"adminGroup,omitempty"`
}

// Names are the variables' names, in a fixed order, those the manifest leaves out empty.
func (e OIDCEnv) Names() []string {
	return []string{e.Issuer, e.Discovery, e.ClientID, e.ClientSecretFile, e.RedirectURI, e.URL, e.AdminGroup}
}

// ClientSecret is the secret HomersLab hands an OIDC app, its client's: no secret of the manifest's may take its name.
const ClientSecret = "oidc-client-secret"

// SignInMethod is one of the three every identity provider provides (spec: Adapter contracts).
type SignInMethod string

const (
	OIDC  SignInMethod = "oidc"
	LDAP  SignInMethod = "ldap"
	Guard SignInMethod = "guard" // the edge signs people in for the app
)

// Names are the app's name on the household's domains, the service and port the edge sends it to, and its exposure.
type Names struct {
	Name     string `yaml:"name" json:"name"` // photos, for photos.<domain>
	Service  string `yaml:"service" json:"service"`
	Port     int    `yaml:"port" json:"port"`
	Exposure string `yaml:"exposure" json:"exposure"` // mesh, the default for every app
	Public   bool   `yaml:"public" json:"public"`     // whether a public path is allowed
}

// Audience is who sees the app at first, and its kid view.
type Audience struct {
	Default string `yaml:"default" json:"default"` // everyone, adults or admins
	KidView string `yaml:"kidView" json:"kidView,omitempty"`
}

// Data is one thing of the app's to back up: a path in a service, or a database dump through a declared command.
// Backup false declares a persistent path that isn't backed up, with the reason.
type Data struct {
	What    string `yaml:"what" json:"what"`
	Service string `yaml:"service" json:"service"`
	Path    string `yaml:"path" json:"path,omitempty"`
	Dump    *Dump  `yaml:"dump" json:"dump,omitempty"`
	Backup  *bool  `yaml:"backup" json:"backup,omitempty"`
	Why     string `yaml:"why" json:"why,omitempty"`
}

// BackedUp says whether the data is backed up.
func (d Data) BackedUp() bool { return d.Backup == nil || *d.Backup }

// Dump is the command that writes a database dump to its standard output.
type Dump struct {
	Command []string `yaml:"command" json:"command"`
	File    string   `yaml:"file" json:"file"`
}

// Secret is one secret HomersLab generates for the app, and the UID that reads it.
type Secret struct {
	Name string `yaml:"name" json:"name"`
	What string `yaml:"what" json:"what"`
	UID  int    `yaml:"uid" json:"uid"`
}

// Health is one check that the app works: an HTTP request to a service, or the compose file's own healthcheck.
type Health struct {
	Service string      `yaml:"service" json:"service"`
	HTTP    *HTTPHealth `yaml:"http" json:"http,omitempty"`
	Compose bool        `yaml:"compose" json:"compose,omitempty"`
}

// HTTPHealth is a request that must answer 2xx.
type HTTPHealth struct {
	Port int    `yaml:"port" json:"port"`
	Path string `yaml:"path" json:"path"`
}

// Resources are what the app needs of its node.
type Resources struct {
	MemoryMiB int    `yaml:"memoryMiB" json:"memoryMiB"`
	DiskGiB   int    `yaml:"diskGiB" json:"diskGiB"`
	Grows     string `yaml:"grows" json:"grows,omitempty"` // what makes it grow: the photo library
	GPU       GPU    `yaml:"gpu" json:"gpu"`
	// CanPause says the app may be paused when its disk is nearly full (ADR 0017).
	CanPause bool `yaml:"canPause" json:"canPause"`
}

// GPU is whether the app wants a GPU, of which makers, and what for.
type GPU struct {
	Need    string   `yaml:"need" json:"need"` // none, optional or required
	Vendors []string `yaml:"vendors" json:"vendors,omitempty"`
	For     string   `yaml:"for" json:"for,omitempty"`
}

// Extra is a permission beyond the floor: the machine's network, a device or a capability, approved at install.
type Extra struct {
	ID         string `yaml:"id" json:"id"`
	Kind       string `yaml:"kind" json:"kind"` // network, device or capability
	Service    string `yaml:"service" json:"service"`
	Device     string `yaml:"device" json:"device,omitempty"`
	Capability string `yaml:"capability" json:"capability,omitempty"`
	Why        string `yaml:"why" json:"why"`
	Optional   bool   `yaml:"optional" json:"optional"`
}

// HostApp is a host app's recipe: its maker's repository and package, per distro.
type HostApp struct {
	Packages []HostPackage `yaml:"packages" json:"packages"`
}

// HostPackage is the package for one distro family, from its maker's repository.
type HostPackage struct {
	Distros    []string `yaml:"distros" json:"distros"` // os-release IDs: fedora, debian, ubuntu, arch
	Repository string   `yaml:"repository" json:"repository"`
	Key        string   `yaml:"key" json:"key"` // the repository's signing key, by URL
	Package    string   `yaml:"package" json:"package"`
}

// UpdateNote is what one version changes, for the admin applying it.
type UpdateNote struct {
	Version string `yaml:"version" json:"version"`
	Notes   string `yaml:"notes" json:"notes"`
}
