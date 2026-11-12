// A name is shown isolated from the text around it (#124): a right-to-left name can't reorder the words beside it.
// On the web that is an element of its own with its direction from its first strong letter: <bdi>, or dir="auto".
import { expect, test } from './primary';

const arabic = 'محمد رضا';
const hebrew = 'שלום כהן';

test('Admin shows a right-to-left name isolated, on the plan and its card', async ({ page, primary }) => {
  await primary.setUp({ you: arabic });
  await page.goto(await primary.adminURL());
  await page.getByRole('button', { name: 'Get Immich: see its install plan' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Install Immich');
  // "Who will see it": the name in an element of its own, and the words around it outside
  const who = page.locator('.row-label', { hasText: 'Adults:' });
  await expect(who.locator('bdi', { hasText: arabic })).toHaveCount(1);
  await expect(who.locator('bdi', { hasText: arabic })).toHaveCSS('unicode-bidi', 'isolate');
  await expect(who).toHaveText(`Adults: ${arabic}`);
  // "A place on Home for ..." on the card
  const card = page.locator('.plan-card li', { hasText: 'A place on Home for' });
  await expect(card.locator('bdi', { hasText: arabic })).toHaveCount(1);
  await expect(card).toHaveText(`A place on Home for ${arabic}`);
});

test('Admin isolates every name in a list of people', async ({ page, primary }) => {
  await primary.setUp({ you: hebrew });
  await page.goto(await primary.adminURL());
  await page.getByRole('button', { name: 'Get Immich: see its install plan' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Install Immich');
  await expect(page.locator('.row-label bdi', { hasText: hebrew })).toHaveCount(1);
});

test('first-time setup isolates the name where it shows it: the field, the picture and the list of conflicts', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  const field = page.getByLabel('Your name', { exact: true });
  await expect(field).toHaveAttribute('dir', 'auto');
  await field.fill(arabic);
  // the picture's initial and its mail chip
  const illo = page.locator('.illo');
  await expect(illo.locator('.illo-initial bdi')).toHaveText('م');
  await page.getByLabel('Your mail address', { exact: true }).fill('mohamed@example.org');
  await expect(illo.locator('.illo-chip bdi', { hasText: 'mohamed@example.org' })).toHaveCount(1);
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Name your household');
  const household = page.getByLabel('The household’s name', { exact: true });
  await expect(household).toHaveAttribute('dir', 'auto');
  await household.fill(`عائلة ${arabic}`);
  await expect(illo.locator('.illo-chip bdi', { hasText: arabic })).toHaveCount(1);
});
