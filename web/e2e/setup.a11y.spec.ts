// First-time setup's screens under axe (WCAG 2.2 AA), in every look a device can ask for, at the projects' phone and
// desktop widths: one walk through the screens per look, one line per screen.
import { accessible, audit, EXPERIMENTAL_WCAG, type Glass, legible, looks, lookName, report, WCAG22AA, wear } from './a11y';
import { expect, test } from './primary';

for (const look of looks) {
  test(`first-time setup passes axe (WCAG 2.2 AA): ${lookName(look)}`, async ({ page, primary }) => {
    test.slow(); // many screens, each settled and audited: about 15 s alone, more beside other tests
    await wear(page, look);
    const h1 = page.getByRole('heading', { level: 1 });
    const next = page.getByRole('button', { name: 'Continue' });

    await page.goto((await primary.newLink()).localUrl.replace(/#.*/, ''));
    await expect(h1).toContainText('This link doesn’t work any more');
    await accessible(page, 'the link doesn’t work');

    // the disk under the manager's database is full when the page opens (#70)
    await page.route('**/api/v1/setup', (route) =>
      route.fulfill({ status: 507, contentType: 'application/json', body: '{"error":"disk-full"}' }));
    await page.goto((await primary.newLink()).localUrl);
    await expect(h1).toContainText('This machine’s disk is full');
    await accessible(page, 'the disk is full');
    await page.unroute('**/api/v1/setup');

    await page.goto((await primary.newLink()).localUrl);
    await expect(h1).toContainText('Let’s set up HomersLab');
    await accessible(page, 'welcome');
    await page.getByText('About this machine').click();
    await accessible(page, 'welcome, about this machine open');

    await page.getByRole('button', { name: 'Get started' }).click();
    await expect(h1).toContainText('What’s your name?');
    await accessible(page, 'your name, empty');
    await page.getByLabel('Your name', { exact: true }).fill('Inês Ferreira');
    await page.getByLabel('Your mail address', { exact: true }).fill('<ines@ferreira.family>'); // the manager refuses it
    await next.click();
    await expect(page.getByText('Type a mail address, like name@example.org.')).toBeVisible();
    await accessible(page, 'your name, refused');
    // every answer the manager refuses says why, and each of those messages is audited: invisible, too long, too many marks
    const name = page.getByLabel('Your name', { exact: true });
    for (const [typed, says] of [['Inês\u202eevil', 'Your name has an invisible character'], ['I'.repeat(65), 'Your name can be 64 characters at most.'],
      [`A${'\u0316\u0317\u0318\u0319\u031a'.repeat(12)}`, 'Your name has too many accents or marks on one letter']] as const) {
      await name.fill(typed);
      await next.click();
      await expect(page.getByText(says)).toBeVisible();
      await accessible(page, `your name, refused: ${says}`);
    }
    await name.fill('Inês Ferreira');
    // the disk fills while an answer is sent: the alert beside the button, the typed answer kept
    await page.getByLabel('Your mail address', { exact: true }).fill('ines@ferreira.family');
    await page.route('**/api/v1/setup/steps/you', (route) =>
      route.fulfill({ status: 507, contentType: 'application/json', body: '{"error":"disk-full"}' }));
    await next.click();
    await expect(page.getByRole('alert')).toContainText('This machine’s disk is full');
    await accessible(page, 'your name, the disk is full');
    await page.unroute('**/api/v1/setup/steps/you');
    await next.click();

    await expect(h1).toContainText('Name your household');
    await accessible(page, 'your household');
    const household = page.getByLabel('The household’s name', { exact: true });
    await household.fill('H'.repeat(65));
    await next.click();
    await expect(page.getByText('The household’s name can be 64 characters at most.')).toBeVisible();
    await accessible(page, 'your household, refused');
    await household.fill('Ferreira\u0316\u0317\u0318\u0319\u031a');
    await next.click();
    await expect(page.getByText('The name has too many accents or marks on one letter')).toBeVisible();
    await accessible(page, 'your household, too many marks');
    await household.fill('The Ferreira household');
    await next.click();

    await expect(h1).toContainText('How everyone signs in');
    await accessible(page, 'how everyone signs in');

    await primary.restartManager();
    await page.goto((await primary.newLink()).localUrl);
    await expect(h1).toContainText('Welcome back');
    await accessible(page, 'welcome back');

    await primary.stopAgent();
    await page.reload();
    await expect(h1).toContainText('Welcome back');
    await page.getByText('About this machine').click();
    await expect(page.getByRole('status')).toBeVisible(); // the agent doesn't answer
    await accessible(page, 'welcome, the agent silent, about this machine open');

    await primary.stopManager();
    await page.evaluate(() => { location.hash = '#k=a-new-link'; }); // a new link arrives, and nothing answers
    // the claim is sent again after 1, 2 and 4 s before setup says so, and offers "Try again"
    await expect(h1).toContainText('Setup can’t reach HomersLab', { timeout: 15_000 });
    await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
    await accessible(page, 'the manager can’t be reached');
  });
}

// The check itself: a sentence too faint to read and a field without a label, planted on the welcome screen.
test('the check catches too little contrast and a field without a label', async ({ page, primary }) => {
  await wear(page, { theme: 'light', motion: 'reduce', transparency: 'reduce' });
  await page.goto((await primary.newLink()).localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
  await page.locator('.as-lead').evaluate((lead) => lead.insertAdjacentHTML('afterend',
    '<p id="planted-faint" style="background: #fff; color: #ccc">A planted sentence</p><input id="planted-unlabelled" type="text">'));
  const { violations, passes, incomplete, inapplicable } = await audit(page);
  // the rules that ran are WCAG 2.0–2.2 A and AA, WCAG 2.2's own (target size, off in axe by default) and axe's
  // experimental WCAG rules (orientation lock, which phones need, among them) included
  const ran = [...violations, ...passes, ...incomplete, ...inapplicable];
  expect(ran.map((r) => r.id)).toEqual(expect.arrayContaining(['target-size', ...EXPERIMENTAL_WCAG]));
  expect(EXPERIMENTAL_WCAG).toContain('css-orientation-lock');
  expect(ran.filter((r) => !r.tags.some((t) => WCAG22AA.includes(t))).map((r) => r.id)).toEqual([]);
  // with every material solid, axe measures the words on the card (the glass's rim doesn't hide them)
  const measured = passes.find((r) => r.id === 'color-contrast')?.nodes.map((n) => n.target.join(' '));
  expect(measured).toEqual(expect.arrayContaining(['#as-title', '.as-lead']));
  expect(violations.map((v) => v.id).sort()).toEqual(['color-contrast', 'label']);
  expect(report(violations)).toMatch(/color-contrast \(serious\)[^]*#planted-faint[^]*label \(critical\)[^]*#planted-unlabelled/);
});

// The pixel check at a glass level: setup offers none (it shows Balanced, the design system's default), Admin and Home
// will offer all four, so the check takes a level, measures there and puts the page's own back.
test('the pixel check measures at the glass level it is given', async ({ page, primary }) => {
  await wear(page, { theme: 'light', motion: 'reduce', transparency: 'no-preference' });
  await page.goto((await primary.newLink()).localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
  const html = page.locator('html');
  const lead = async (glass?: Glass) => {
    const { runs, problems } = await legible(page, glass);
    await expect(html, 'the page’s own glass level is back').not.toHaveAttribute('data-glass');
    return { level: problems.level, contrast: runs.find((r) => r.where === 'p.as-lead')?.contrast ?? NaN };
  };
  const own = await lead();
  const at = { clear: await lead('clear'), balanced: await lead('balanced'), frosted: await lead('frosted'), solid: await lead('solid') };
  expect([own, at.clear, at.balanced, at.frosted, at.solid].map((m) => m.level)).toEqual(['glass Balanced (--glass: 0.5)',
    'glass Clear (--glass: 0)', 'glass Balanced (--glass: 0.5)', 'glass Frosted (--glass: 1)', 'glass Solid (--glass: 1)']);
  expect(own.contrast).toBeCloseTo(at.balanced.contrast, 1);
  // the level changes what is behind the words: Clear lets the wallpaper through, Solid is white
  expect(Math.abs(at.clear.contrast - at.frosted.contrast)).toBeGreaterThan(0.1);
  expect(Math.abs(at.frosted.contrast - at.solid.contrast)).toBeGreaterThan(0.1);
});

// What reads well passes, wherever it sits: a button in a shadow root in the card, words with a hairline through their
// box, a select's option and a button's value, and large text at 3.2:1 (large means 24px, or 14pt (56/3 px) in bold).
test('the pixel check passes words that read well', async ({ page, primary }) => {
  await wear(page, { theme: 'light', motion: 'reduce', transparency: 'reduce' });
  await page.goto((await primary.newLink()).localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
  await page.locator('.as-lead').evaluate((lead) => {
    lead.insertAdjacentHTML('afterend', ['<div id="ok-host"></div><p id="ok-lined">Words with a line through</p>',
      '<select id="ok-select" aria-label="A select"><option>A shown option</option></select>',
      '<input id="ok-submit" type="submit" value="A value">',
      '<p id="ok-large">Large words</p><p id="ok-bold">Bold words</p><p id="ok-edge">Nearly large</p>',
      '<div id="ok-shadowed"><p>Words in a shadowed card</p></div>',
      '<p id="ok-tip">Tip <span id="ok-tipped" data-tip="A hint">here</span></p>',
      '<p id="ok-letter">Drop <b>bold</b> rest</p>'].join(''));
    document.querySelector('#ok-host')?.attachShadow({ mode: 'open' }).append(Object.assign(document.createElement('button'), { textContent: 'A shadow button' }));
  });
  const css: [string, string, string][] = [
    // a 1px line, #777 (2.8:1 under #333), through the middle of a 16px line of words
    ['#ok-lined', 'color', '#333'], ['#ok-lined', 'font-size', '16px'],
    ['#ok-lined', 'background-image', 'linear-gradient(transparent calc(50% - 0.5px), #777 calc(50% - 0.5px), #777 calc(50% + 0.5px), transparent calc(50% + 0.5px))'],
    ['#ok-large', 'font-size', '24px'], ['#ok-large', 'font-weight', '400'], ['#ok-large', 'color', '#909090'], // 3.19:1 on white
    ['#ok-bold', 'font-size', `${56 / 3 + 0.01}px`], ['#ok-bold', 'font-weight', '700'], ['#ok-bold', 'color', '#909090'],
    ['#ok-edge', 'font-size', '18.66px'], ['#ok-edge', 'font-weight', '700'], ['#ok-edge', 'color', '#909090'],
  ];
  for (const [sel, prop, value] of css) {
    await page.locator(sel).evaluate((el, pv) => (el as HTMLElement).style.setProperty(pv.prop, pv.value, 'important'), { prop, value });
  }
  await page.evaluate(() => {
    const sheet = new CSSStyleSheet();
    sheet.replaceSync([
      '#ok-shadowed { filter: drop-shadow(0 1px 2px rgba(0, 0, 0, 0.2)); }', // a shadow changes no word's pixels
      '#ok-tipped::after { content: attr(data-tip); opacity: 0; } #ok-tipped::before { content: "Hidden"; visibility: hidden; }', // not shown
      '#ok-letter::first-letter { color: #555; }', // the paragraph's first letter only
    ].join('\n'));
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  });
  const { runs, problems } = await legible(page);
  expect(problems.level).toBe('glass Solid (reduced transparency)');
  const run = (text: string) => runs.find((r) => r.text === text);
  for (const text of ['A shadow button', 'Words with a line through', 'A shown option', 'A value', 'Large words', 'Bold words',
    'Words in a shadowed card', 'bold', 'rest']) {
    const r = run(text);
    expect(r, text).toBeDefined();
    expect(r?.unmeasured, text).toEqual([]);
    expect(r?.contrast, text).toBeGreaterThanOrEqual(r?.needs ?? Infinity);
  }
  expect(run('Large words')?.needs).toBe(3);
  expect(run('Large words')?.contrast).toBeLessThan(3.5);
  expect(run('Bold words')?.needs).toBe(3);
  expect(run('Nearly large')?.needs).toBe(4.5);
  expect([...new Set(run('Drop')?.unmeasured)]).toEqual(['its first letter has a colour of its own (::first-letter)']);
  expect(runs.filter((r) => r.where.startsWith('span#ok-tipped:')).map((r) => r.where)).toEqual([]);
});
