import { test as base, expect, chromium } from '@playwright/test'
import { readFileSync } from 'node:fs'

const test = base.extend({
  browser: [async ({}, use) => {
    const browser = process.env.POCKET_ASK_CDP
      ? await chromium.connectOverCDP(process.env.POCKET_ASK_CDP)
      : await chromium.launch()
    await use(browser)
    await browser.close()
  }, { scope: 'worker' }],
})
const panel = readFileSync('../internal/server/ask.html', 'utf8')
const renderer = readFileSync('dist/ask-markdown.js', 'utf8')
const answer = `## Spending overview

**This month** with *notes*, ~~corrections~~ and [source](https://money.example/items/42).

| Category | Amount | Transactions |
|---|---:|---:|
| Home and household expenses | 1,054.25 | 28 |
| Groceries | 295.26 | 25 |

1. Review the categories
   - Keep the original receipt
2. Check the totals

- [x] Categorised
- [ ] Still to review

> These are example figures.

\`\`\`json
{"long_value": "${'x'.repeat(180)}"}
\`\`\`

Inline \`code\` and an escaped pipe: a\\|b.
`

test.beforeEach(async ({ page }) => {
  await page.route('**/*', async route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/ask') return route.fulfill({ contentType: 'text/html', body: panel })
    if (url.pathname === '/ask-markdown.js') return route.fulfill({ contentType: 'text/javascript', body: renderer })
    if (url.pathname.endsWith('/status')) return route.fulfill({ json: { connected: true, user_id: 1, ai_url: 'https://ai.example', name: 'Test' } })
    const thread = { id: 'test', title: 'Example table', source_ref: 'https://core.test/item/42', status: 'idle' }
    if (url.pathname.endsWith('/threads')) return route.fulfill({ json: [thread] })
    if (url.pathname.endsWith('/threads/test')) return route.fulfill({ json: { thread, items: [
      { key: 'u', kind: 'user', text: 'Show **this** literally.' },
      { key: 'a', kind: 'assistant', text: answer },
    ] } })
    return route.abort()
  })
  await page.goto('https://core.test/ask#' + encodeURIComponent(JSON.stringify({ app: 'money', origin: 'https://core.test' })))
  await page.evaluate(() => window.postMessage({ type: 'pocket-ask-context', source: { app: 'money', url: 'https://core.test/item/42', title: 'Example item' } }, location.origin))
  await page.getByRole('button', { name: 'Example table' }).click()
  await expect(page.locator('.assistant .markdown')).toBeVisible()
})

test('renders tables, nested lists, tasks, quotes, links and code; preserves user text', async ({ page }) => {
  const md = page.locator('.assistant .markdown')
  await expect(md.locator('table')).toHaveCount(1)
  await expect(md.locator('tbody tr')).toHaveCount(2)
  await expect(md.locator('tbody tr').first().locator('td').nth(1)).toHaveCSS('text-align', 'right')
  await expect(md.locator('h2')).toHaveText('Spending overview')
  await expect(md.locator('ol > li')).toHaveCount(2)
  await expect(md.locator('ol ul > li')).toHaveText('Keep the original receipt')
  await expect(md.locator('input:checked')).toHaveCount(1)
  await expect(md.locator('input:enabled')).toHaveCount(0)
  await expect(md.locator('blockquote')).toContainText('example figures')
  await expect(md.getByRole('link', { name: 'source' })).toHaveAttribute('rel', 'noopener noreferrer')
  await expect(md.locator('pre code')).toContainText('long_value')
  await expect(page.locator('.user .text')).toHaveText('Show **this** literally.')
  await expect(page.locator('.user strong')).toHaveCount(0)
  // An unchanged poll must leave the answer DOM and its scroll position intact.
  await md.locator('table').evaluate(el => el.setAttribute('data-stability', 'same'))
  await page.waitForTimeout(2200)
  await expect(md.locator('table')).toHaveAttribute('data-stability', 'same')
})

test('sanitizes hostile markup and unsafe links; tolerates incomplete streamed Markdown', async ({ page }) => {
  const result = await page.evaluate(() => {
    const node = document.querySelector('.assistant .markdown') as HTMLElement
    const api = (window as any).PocketAskMarkdown
    api.render(node, '[bad](javascript:alert%281%29)\n\n<img src=x onerror=alert(1)><script>alert(1)</script>\n\n<iframe src="https://evil.example"></iframe>')
    const unsafe = node.querySelector('script,img,iframe,[onerror],[style],a[href^="javascript:"]') !== null
    api.render(node, '**Keep formatting** [bad](http://[)')
    const malformedLink = node.querySelector('strong')?.textContent === 'Keep formatting' && !node.querySelector('a[href]')
    const samples = ['| A | B |\n|---|---:|\n| one |', '**unfinished', '```json\n{"a":', '[unfinished](https://']
    const preserved = samples.every(source => { api.render(node, source); return !!node.textContent })
    api.render(node, '| A | B |\n|---|---|\n| escaped \\| pipe | `code` |')
    return { unsafe, preserved, malformedLink, cells: node.querySelectorAll('tbody td').length, first: node.querySelector('tbody td')?.textContent }
  })
  expect(result).toEqual({ unsafe: false, preserved: true, malformedLink: true, cells: 2, first: 'escaped | pipe' })
})

test('contains wide tables and code on small phones, desktop panels and both themes', async ({ page }) => {
  for (const width of [320, 390, 460]) for (const dark of [false, true]) {
    await page.setViewportSize({ width, height: 700 })
    await page.evaluate(dark => document.documentElement.classList.toggle('dark', dark), dark)
    const dimensions = await page.evaluate(() => {
      const node = document.querySelector('.assistant .markdown') as HTMLElement
      ;(window as any).PocketAskMarkdown.render(node, '| Category | Amount | Transactions | Reference |\n|---|---:|---:|---|\n| Home and household | 123456.78 | 200 | '+ 'a'.repeat(150) + ' |\n\n```\n'+ 'b'.repeat(200)+'\n```')
      const table = node.querySelector('.table-scroll') as HTMLElement, code = node.querySelector('pre')!
      const content = document.querySelector('#content')!.getBoundingClientRect(), status = document.querySelector('#status')!.getBoundingClientRect()
      return { page: document.documentElement.scrollWidth <= innerWidth, table: table.scrollWidth > table.clientWidth, code: code.scrollWidth > code.clientWidth, separateStatus: content.bottom <= status.top }
    })
    expect(dimensions).toEqual({ page: true, table: true, code: true, separateStatus: true })
  }
})

test('photo draft previews the image and sends it only with the edited question', async ({ page }) => {
  const posts: { path: string; body: any }[] = []
  const thread = { id: 'photo-thread', title: 'Screenshot.png', status: 'idle' }
  await page.route('**/api/v1/ask/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (route.request().method() === 'POST') posts.push({ path, body: route.request().postDataJSON() })
    if (path.endsWith('/status')) return route.fulfill({ json: { connected: true, user_id: 1, name: 'Test', ai_url: 'https://ai.example' } })
    if (path.endsWith('/threads')) return route.fulfill({ json: route.request().method() === 'POST' ? thread : [] })
    if (path.endsWith('/messages')) return route.fulfill({ json: {} })
    return route.fulfill({ json: { thread, items: [] } })
  })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('about:blank')
  await page.goto('https://core.test/ask#' + encodeURIComponent(JSON.stringify({ app: 'photos', origin: 'https://core.test' })))
  const source = { app: 'photos', url: 'https://core.test/photo/42', title: 'Screenshot.png', selection: 'Source: Nextcloud', image: {
    name: 'Screenshot.png', data_url: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jN1cAAAAASUVORK5CYII=',
  } }
  const sendContext = () => page.evaluate(source => window.postMessage({ type: 'pocket-ask-context', source, prompt: 'Describe this photo and explain any visible text.' }, location.origin), source)
  await sendContext()
  await expect(page.getByLabel('Your question')).toHaveValue('Describe this photo and explain any visible text.')
  await expect(page.locator('#photo-context')).toBeVisible()
  await expect(page.locator('#photo-preview')).toHaveAttribute('src', source.image.data_url)
  await expect(page.locator('#context')).toContainText('Photos · Screenshot.png')
  expect(posts).toHaveLength(0)
  await page.getByLabel('Your question').fill('What does this error mean?')
  await sendContext()
  await expect(page.getByLabel('Your question')).toHaveValue('What does this error mean?')
  await page.getByRole('button', { name: 'Ask ↗', exact: true }).click()
  await expect.poll(() => posts.length).toBe(1)
  expect(posts[0].body).toMatchObject({ text: 'What does this error mean?', image: source.image, model: 'sonnet', source: { app: 'photos', title: 'Screenshot.png' } })
  expect(posts[0].body.source.image).toBeUndefined()
  await expect(page.locator('#photo-context')).toBeHidden()
  await page.getByLabel('Your question').fill('Can you explain it more simply?')
  await page.getByRole('button', { name: 'Ask ↗', exact: true }).click()
  await expect.poll(() => posts.length).toBe(2)
  expect(posts[1].body).toEqual({ text: 'Can you explain it more simply?' })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('button', { name: 'New question', exact: true }).click()
  await expect(page.locator('#photo-context')).toBeVisible()
  await expect(page.getByLabel('Your question')).toHaveValue('Describe this photo and explain any visible text.')
})
