/**
 * Render local design screenshots. Requires Node.js and Playwright with Chromium.
 * Usage: node docs/proposals/interactive-cli/render.mjs [OUTPUT_DIRECTORY]
 * Default output: ignored results/interactive-cli-design/ at the repository root.
 * Supply Playwright through the local runtime or an external NODE_PATH; this
 * generator does not install packages, launch providers or connect to services.
 * SDLC_PREVIEW_CHROMIUM optionally selects an already installed Chromium binary.
 */
import { createRequire } from 'node:module';
import { mkdir, writeFile } from 'node:fs/promises';
import { dirname, resolve, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');
const sourceDirectory = dirname(fileURLToPath(import.meta.url));
const repository = resolve(sourceDirectory, '../../..');
const destination = resolve(process.argv[2] || join(repository, 'results/interactive-cli-design'));
await mkdir(destination, { recursive: true });
const browser = await chromium.launch({ headless: true, executablePath: process.env.SDLC_PREVIEW_CHROMIUM || undefined });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1050 }, deviceScaleFactor: 1 });
  await page.route('**/*', route => {
    const scheme = new URL(route.request().url()).protocol;
    return ['file:', 'data:', 'about:'].includes(scheme) ? route.continue() : route.abort();
  });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const source = pathToFileURL(join(sourceDirectory, 'preview.html'));
  source.searchParams.set('capture', '1');
  source.searchParams.set('terminal', '1');
  await page.goto(source.href);
  const scenes = await page.locator('#scene-select option').evaluateAll(options => options.map(o => ({ id: o.value, title: o.textContent })));
  const captures = [];
  const capture = async (scene, filename) => {
    await page.evaluate(() => document.fonts.ready);
    const viewport = page.viewportSize();
    await page.screenshot({ path: join(destination, filename), fullPage: false, animations: 'disabled' });
    const layout = await page.locator('#terminal').evaluate(element => {
      const body = element.querySelector('.terminal-body');
      const prompt = element.querySelector('.prompt-area').getBoundingClientRect();
      const rect = element.getBoundingClientRect();
      return {
        terminalWidth: element.clientWidth,
        terminalHeight: element.clientHeight,
        horizontalOverflow: document.documentElement.scrollWidth > innerWidth || element.scrollWidth > element.clientWidth,
        transcriptScrolls: body.scrollHeight > body.clientHeight + 1,
        fillsViewport: Math.abs(rect.width - innerWidth) <= 2 && Math.abs(rect.height - innerHeight) <= 2,
        promptVisible: prompt.top >= 0 && prompt.bottom <= innerHeight + 1
      };
    });
    if (layout.horizontalOverflow || !layout.fillsViewport || !layout.promptVisible) {
      throw new Error(`Layout failed for ${filename}: ${JSON.stringify(layout)}`);
    }
    captures.push({ ...scene, filename, viewport, ...layout });
  };
  for (const scene of scenes) {
    source.searchParams.set('scene', scene.id);
    await page.goto(source.href);
    await page.locator(`#terminal[data-scene="${scene.id}"]`).waitFor();
    const filename = `${scene.id}.png`;
    await capture(scene, filename);
  }
  source.searchParams.set('scene', '15-projects');
  await page.goto(source.href);
  await page.locator('[data-action="scope-all"]').click();
  await capture({ id: '11-dashboard', title: 'Dashboard with all-projects history' }, 'dashboard-all-projects.png');
  source.searchParams.set('scene', '05-ticket-completion');
  await page.goto(source.href);
  for (const viewport of [{ width: 960, height: 640 }, { width: 720, height: 520 }, { width: 1600, height: 1100 }]) {
    await page.setViewportSize(viewport);
    await page.waitForTimeout(100);
    await capture({ id: '05-ticket-completion', title: `Ticket lookahead after live resize · ${viewport.width} × ${viewport.height}` }, `resize-tickets-${viewport.width}x${viewport.height}.png`);
  }
  source.searchParams.set('scene', '12-conversation');
  await page.goto(source.href);
  await page.locator('#agent-filter').selectOption('claude');
  await page.setViewportSize({ width: 960, height: 640 });
  await page.waitForTimeout(100);
  await capture({ id: '12-conversation', title: 'Filtered conversation after live resize · 960 × 640' }, 'resize-conversation-960x640.png');
  if (errors.length) throw new Error(`Prototype JavaScript errors: ${errors.join('; ')}`);
  const escape = text => text.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const gallery = `<!doctype html><html lang="en"><meta charset="utf-8"><title>SDLC CLI screenshots</title><style>body{margin:32px;background:#111815;color:#dce8e1;font:16px system-ui}h1{font-size:26px}p{color:#a5b8ac}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(420px,1fr));gap:24px}figure{margin:0}img{width:100%;border:1px solid #395145}figcaption{margin:10px 0 20px}a{color:#91dfb0}</style><h1>SDLC interactive CLI screenshots</h1><p>Design prototype. Invented data. No SDLC or provider commands executed.</p><div class="grid">${captures.map(c => `<figure><a href="${escape(c.filename)}"><img src="${escape(c.filename)}" alt="${escape(c.title)}"></a><figcaption>${escape(c.title)}</figcaption></figure>`).join('')}</div></html>`;
  await writeFile(join(destination, 'index.html'), gallery);
  await writeFile(join(destination, 'manifest.json'), JSON.stringify({ simulated: true, terminalOnly: true, scenes: scenes.length, captures }, null, 2) + '\n');
  await page.setViewportSize({ width: 1440, height: 1050 });
  await page.goto(pathToFileURL(join(destination, 'index.html')).href);
  await page.screenshot({ path: join(destination, 'overview.png'), fullPage: true, animations: 'disabled' });
  console.log(JSON.stringify({ scenes: scenes.length, screenshots: captures.length, output: destination, layouts: captures }, null, 2));
} finally {
  await browser.close();
}
