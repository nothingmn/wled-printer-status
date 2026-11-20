// The driven harness: the module's own Layout screen, in a real Chromium, against a stub transport.
//
// What it asserts, and why each one is here rather than in a unit test:
//
//   1. the clock control exists, in the lines panel, beside the seven toggles;
//   2. changing it writes `clock` on the layout route and the screen reports the value's origin;
//   3. the fit verdict follows it — on a 64-column panel the 12-hour spelling is refused and nothing
//      is written, which is the refusal an overfilled panel already gets;
//   4. the seven line toggles still toggle and still say Saved.;
//   5. the page does not move after the first Saved (the one-time lift is expected);
//   6. the three light tab strips are on the three screens that carry one;
//   7. Diagnostics' build line names the release and the store schema;
//   8. no raw catalogue key is on screen, and no console error was logged.
//
// The last two are the regression pair from the previous release: a screen that printed `layout.clock`
// where a sentence belongs, or a component that threw, is what a harness catches and a type check does
// not.
import { spawn } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = fileURLToPath(new URL('.', import.meta.url));
const pw = await import(process.env.PW_ENTRY ?? '/pw/node_modules/playwright/index.js');
// `playwright` is a CommonJS package, so the dynamic import hands back its namespace under `default`.
const { chromium } = pw.chromium === undefined ? pw.default : pw;

const PORT = Number(process.env.PORT ?? 4173);
const BASE = `http://127.0.0.1:${PORT}`;
const CATALOGUE = JSON.parse(readFileSync(join(here, '..', 'src', 'locales', 'en.json'), 'utf8'));

const results = [];
const check = (name, ok, detail = '') => {
	results.push({ name, ok: Boolean(ok), detail });

	return Boolean(ok);
};

const server = spawn('node', [join(here, 'serve.mjs')], {
	env: { ...process.env, PORT: String(PORT) },
	stdio: ['ignore', 'pipe', 'pipe'],
});

const waitForServer = async () => {
	for (let i = 0; i < 60; i += 1) {
		try {
			const answer = await fetch(BASE);
			if (answer.ok) return;
		} catch {
			// not up yet
		}

		await new Promise((resolve) => setTimeout(resolve, 250));
	}

	throw new Error('the harness server never answered');
};

const browser = await chromium.launch({
	executablePath: process.env.CHROME ?? '/usr/bin/chromium',
	args: ['--no-sandbox', '--disable-dev-shm-usage', '--font-render-hinting=none'],
});

const context = await browser.newContext({ viewport: { width: 1280, height: 1400 } });

/** One page load, with its console errors collected. */
const open = async (hash) => {
	const page = await context.newPage();
	const errors = [];

	page.on('console', (message) => {
		if (message.type() === 'error') errors.push(`console: ${message.text()}`);
	});
	page.on('pageerror', (problem) => errors.push(`pageerror: ${problem.message}`));

	await page.goto(`${BASE}/#${hash}`, { waitUntil: 'domcontentloaded' });

	return { page, errors };
};

const noRawKey = async (page) => {
	const text = await page.evaluate(() => document.body.innerText);

	return Object.keys(CATALOGUE).filter((key) => text.includes(key));
};

const tabsOn = async (page) =>
	page.evaluate(() =>
		[...document.querySelectorAll('nav')]
			.filter((nav) => nav.querySelectorAll('button').length > 0)
			.map((nav) => ({ label: nav.getAttribute('aria-label') ?? '', count: nav.querySelectorAll('button').length })),
	);

/** The absolute page Y of the binding's own card: what "the page did not move" is measured on. */
const cardTop = async (page) =>
	page.evaluate(() => {
		const card = document.querySelector('[data-binding]');

		return card === null ? -1 : Math.round(card.getBoundingClientRect().top + window.scrollY);
	});

const noticeText = async (page) =>
	page.evaluate(() => document.querySelector('[role="status"]')?.textContent?.trim() ?? '');

try {
	await waitForServer();

	// -----------------------------------------------------------------------------------------
	// The Layout screen, on the 256-column panel.
	// -----------------------------------------------------------------------------------------
	const layout = await open('layout');
	const page = layout.page;

	await page.waitForSelector('#layout-clock', { timeout: 20000 });
	await page.waitForSelector('[data-binding]', { timeout: 20000 });

	// The innermost panel with that heading: the page is one `<section>` and every card in it is
	// another, so a filter alone matches both the card and the page.
	const linesPanel = page
		.locator('section:not(:has(section))')
		.filter({ has: page.locator('h2', { hasText: 'Lines under the picture' }) });

	check('the lines panel is on the page', (await linesPanel.count()) === 1, `found ${await linesPanel.count()}`);

	const toggles = linesPanel.locator('input[type="checkbox"]');
	check('the seven line toggles are still there', (await toggles.count()) === 7, `found ${await toggles.count()}`);

	const clockInLines = linesPanel.locator('#layout-clock');
	check(
		'the clock control is beside the lines',
		(await clockInLines.count()) === 1,
		`found ${await clockInLines.count()} control(s) in the lines panel`,
	);

	const clockOptions = await page.evaluate(() =>
		[...(document.querySelector('#layout-clock')?.querySelectorAll('option') ?? [])].map((one) => ({
			value: one.value,
			label: one.textContent?.trim() ?? '',
		})),
	);
	check(
		'the control offers the two clocks and starts at 24-hour',
		clockOptions.length === 2 &&
			clockOptions[0].value === '24-hour' &&
			clockOptions[1].value === '12-hour' &&
			(await page.evaluate(() => document.querySelector('#layout-clock')?.value)) === '24-hour',
		JSON.stringify(clockOptions),
	);

	const clockLabel = await page.evaluate(() => document.querySelector('label[for="layout-clock"]')?.textContent?.trim() ?? '');
	check('the control says whose value it is showing', /default$|·\s*default/.test(clockLabel), clockLabel);

	// The seven toggles, one at a time: on and off, each write reaching the route and each answer
	// landing as "Saved.".
	const LINES = ['remaining', 'ends', 'temperatures', 'elapsed', 'material', 'printer_name', 'layer'];
	const before = await cardTop(page);

	let firstSavedTop = -1;
	let togglesOk = true;
	let savedOk = true;

	for (let index = 0; index < LINES.length; index += 1) {
		const box = toggles.nth(index);
		// The label is what a person presses — the control is a 16-pixel box inside a row of words —
		// so the harness presses the same thing. Only the rows that wrap a checkbox are the seven
		// lines; the clock's own Field label is a different element.
		const label = linesPanel.locator('label:has(input[type="checkbox"])').nth(index);

		// Where the row starts is the fixture's business — the ends line starts switched on, as a
		// binding somebody has laid out looks — so the assertion is that a press moves it to the
		// other value and back, not that it starts off.
		const start = await box.isChecked();

		for (const wanted of [!start, start]) {
			await label.click();
			await page.waitForTimeout(120);

			const state = await box.isChecked();
			const said = await noticeText(page);

			if (state !== wanted) {
				togglesOk = false;
				check(`line ${LINES[index]} toggles to ${wanted}`, false, `it reads ${state}`);
			}

			if (!said.startsWith('Saved.')) {
				savedOk = false;
				check(`line ${LINES[index]} says Saved.`, false, said);
			}

			if (index === 0 && wanted === !start) {
				await page.waitForTimeout(150);
				firstSavedTop = await cardTop(page);
			}
		}
	}

	const bodies = await page.evaluate(() => window.__harness.written.map((one) => one.body));
	const lineBodies = bodies.filter((one) => one !== null && typeof one === 'object');
	check('every line toggle was still a write to the layout route', lineBodies.length === 14, `${lineBodies.length} writes`);
	check('the seven line toggles still toggle', togglesOk);
	check('every line toggle says Saved.', savedOk);

	const after = await cardTop(page);
	check(
		'the page lifted once for the Saved line and then stayed put',
		firstSavedTop > before && after === firstSavedTop,
		`before ${before}, after the first Saved ${firstSavedTop}, at the end ${after}`,
	);

	// The clock, on the wide panel: the write goes out and the answer says the value is the store's.
	const writesBefore = await page.evaluate(() => window.__harness.written.length);
	await page.selectOption('#layout-clock', '12-hour');
	await page.waitForTimeout(250);

	const clockWrite = await page.evaluate(() => {
		const held = window.__harness.written;

		return held.length === 0 ? null : held[held.length - 1];
	});
	check(
		'changing the clock writes the field on the layout route',
		(await page.evaluate(() => window.__harness.written.length)) === writesBefore + 1 &&
			clockWrite?.body?.clock === '12-hour' &&
			clockWrite?.path === '/bindings/printer-a/layout',
		JSON.stringify(clockWrite),
	);

	const afterLabel = await page.evaluate(() => document.querySelector('label[for="layout-clock"]')?.textContent?.trim() ?? '');
	check('the control now reports the value as yours', /yours/.test(afterLabel), afterLabel);

	const wideVerdict = await page.evaluate(() =>
		[...document.querySelectorAll('[data-binding] span')]
			.map((one) => one.textContent?.trim() ?? '')
			.filter((one) => one === 'fits' || one === 'does not fit'),
	);
	check(
		'a 12-hour clock still fits the 256-column panel',
		wideVerdict.length === 1 && wideVerdict[0] === 'fits',
		JSON.stringify(wideVerdict),
	);
	check(
		'no clock refusal is shown where the clock fits',
		(await page.locator('[data-refusal="clock"]').count()) === 0,
	);
	check(
		'the lines group names the clock it is drawing',
		(await page.evaluate(() => document.body.innerText.includes('12-hour · 6:10 PM'))),
	);

	// ---------------------------------------------------------------------------------------
	// The 64-column panel: the same control, and the verdict that follows it.
	// ---------------------------------------------------------------------------------------
	const tabs = page.locator('nav button');
	check('the Layout screen tabs its lights', (await tabs.count()) === 2, `found ${await tabs.count()}`);

	await tabs.nth(1).click();
	await page.waitForFunction(() => document.querySelector('[data-binding]')?.getAttribute('data-binding') === 'printer-b', {
		timeout: 20000,
	});
	await page.waitForSelector('#layout-clock');

	const narrowBefore = await page.evaluate(() =>
		[...document.querySelectorAll('[data-binding] span')]
			.map((one) => one.textContent?.trim() ?? '')
			.filter((one) => one === 'fits' || one === 'does not fit'),
	);
	check(
		'a 24-hour clock fits the 64-column panel',
		narrowBefore.length === 1 && narrowBefore[0] === 'fits',
		JSON.stringify(narrowBefore),
	);

	const narrowWrites = await page.evaluate(() => window.__harness.written.length);
	await page.selectOption('#layout-clock', '12-hour');
	await page.waitForTimeout(250);

	const refusal = page.locator('[data-refusal="clock"]');
	check('turning twelve-hour on where it does not fit shows the refusal', (await refusal.count()) === 1);

	const refusalText = (await refusal.count()) === 1 ? await refusal.innerText() : '';
	check(
		'the refusal names the width it needs and the panel it has',
		/\b77\b/.test(refusalText) && /\b64\b/.test(refusalText) && /12-hour/.test(refusalText),
		refusalText,
	);
	check(
		'nothing was written for a clock that does not fit',
		(await page.evaluate(() => window.__harness.written.length)) === narrowWrites,
		`${await page.evaluate(() => window.__harness.written.length)} writes`,
	);
	check(
		'the fit verdict says the layout does not fit',
		await page.evaluate(() =>
			[...document.querySelectorAll('[data-binding] span, [data-binding] p')].some(
				(one) => one.textContent?.trim() === 'does not fit',
			),
		),
	);

	const rawLayout = await noRawKey(page);
	check('no raw catalogue key is on the Layout screen', rawLayout.length === 0, rawLayout.join(', '));
	check('the Layout screen logged no console error', layout.errors.length === 0, layout.errors.join(' | '));

	// -----------------------------------------------------------------------------------------
	// The other two screens that tab a light.
	// -----------------------------------------------------------------------------------------
	const events = await open('events');
	await events.page.waitForSelector('nav button', { timeout: 20000 });
	const eventTabs = await tabsOn(events.page);
	check(
		'the Events screen tabs its lights',
		eventTabs.some((one) => one.count === 2),
		JSON.stringify(eventTabs),
	);
	const rawEvents = await noRawKey(events.page);
	check('no raw catalogue key is on the Events screen', rawEvents.length === 0, rawEvents.join(', '));
	check('the Events screen logged no console error', events.errors.length === 0, events.errors.join(' | '));

	const diagnostics = await open('diagnostics');
	await diagnostics.page.waitForSelector('[data-build]', { timeout: 20000 });
	const diagnosticTabs = await tabsOn(diagnostics.page);
	check(
		'the Diagnostics screen tabs its lights',
		diagnosticTabs.some((one) => one.count === 2),
		JSON.stringify(diagnosticTabs),
	);

	const buildLine = await diagnostics.page.evaluate(
		() => document.querySelector('[data-build]')?.textContent?.replace(/\s+/g, ' ').trim() ?? '',
	);
	check(
		'Diagnostics names the release and the store schema',
		buildLine.includes('0.7.0') && /store schema 10/.test(buildLine),
		buildLine,
	);
	const rawDiagnostics = await noRawKey(diagnostics.page);
	check('no raw catalogue key is on the Diagnostics screen', rawDiagnostics.length === 0, rawDiagnostics.join(', '));
	check('the Diagnostics screen logged no console error', diagnostics.errors.length === 0, diagnostics.errors.join(' | '));

	// A picture of the refused panel, kept in the build directory so a run leaves no litter in the
	// tree: `dist/` is thrown away and made again by the next build.
	await page.screenshot({ path: join(here, 'dist', 'layout-narrow.png'), fullPage: true });
} catch (problem) {
	check('the harness ran to the end', false, problem instanceof Error ? problem.stack ?? problem.message : String(problem));
} finally {
	await browser.close();
	server.kill();
}

const failed = results.filter((one) => !one.ok);
for (const one of results) {
	console.log(`${one.ok ? 'ok  ' : 'FAIL'} ${one.name}${one.detail === '' ? '' : ` — ${one.detail}`}`);
}

console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length === 0 ? 0 : 1);
