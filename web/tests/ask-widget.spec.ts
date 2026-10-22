import { test, expect } from '@playwright/test'
import { readFileSync } from 'node:fs'

const widget = readFileSync('../sdk/ask-widget.js', 'utf8')

test.beforeEach(async ({ page }) => {
  await page.route('https://app.test/**', route => route.fulfill({
    contentType: 'text/html', body: '<button id="photo">Ask about photo</button>',
  }))
  await page.route('https://core.test/ask**', route => route.fulfill({
    contentType: 'text/html', body: '<textarea aria-label="Draft"></textarea>',
  }))
  await page.goto('https://app.test/')
  await page.addScriptTag({ content: `(()=>{const config={app:'photos',core:'https://core.test'};${widget}})()` })
  await page.evaluate(() => {
    document.querySelector('#photo')!.addEventListener('click', () => window.dispatchEvent(new Event('pocket-ask')))
  })
})

test('opening and dismissal preserve drafts and restore focus on desktop and mobile', async ({ page }) => {
  for (const width of [1200, 390]) {
    await page.setViewportSize({ width, height: 844 })
    const panel = page.getByRole('dialog', { name: 'Pocket Ask' })
    await page.locator('#photo').click()
    await expect(panel).toBeVisible()
    await page.frameLocator('iframe').getByLabel('Draft').fill('Keep my question')
    await page.getByRole('button', { name: 'Close Pocket Ask' }).click()
    await expect(panel).toBeHidden()
    await expect(page.locator('#photo')).toBeFocused()
    await page.locator('#photo').click()
    await expect(page.frameLocator('iframe').getByLabel('Draft')).toHaveValue('Keep my question')
    await page.keyboard.press('Escape')
    await expect(panel).toBeHidden()
    await expect(page.locator('#photo')).toBeFocused()
  }
})

test('reopening during dismissal cancels hiding and leaves the panel interactive', async ({ page }) => {
  await page.locator('#photo').click()
  await expect(page.frameLocator('iframe').getByLabel('Draft')).toBeVisible()
  // Trigger both in one browser turn to exercise an interrupted dismissal.
  await page.evaluate(() => {
    const root = document.querySelector('pocket-ask-launcher')!.shadowRoot!
    ;(root.querySelector('.close') as HTMLButtonElement).click()
    window.dispatchEvent(new Event('pocket-ask'))
  })
  await page.waitForTimeout(350)
  await expect(page.getByRole('dialog', { name: 'Pocket Ask' })).toBeVisible()
  await page.frameLocator('iframe').getByLabel('Draft').fill('Still works')
  await page.getByRole('button', { name: 'Close Pocket Ask' }).click()
  await expect(page.getByRole('dialog', { name: 'Pocket Ask' })).toBeHidden()
})

test('reduced motion opens and closes immediately without an animation', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.locator('#photo').click()
  const state = await page.evaluate(() => {
    const root = document.querySelector('pocket-ask-launcher')!.shadowRoot!
    const panel = root.querySelector('.panel') as HTMLElement
    const opened = !panel.hidden && !panel.inert
    const animations = panel.getAnimations().length
    ;(root.querySelector('.close') as HTMLButtonElement).click()
    return { opened, animations, closed: panel.hidden }
  })
  expect(state).toEqual({ opened: true, animations: 0, closed: true })
})


test('closing returns keyboard focus to the floating launcher', async ({ page }) => {
  const launch = page.getByRole('button', { name: 'Ask AI about this page' })
  await launch.click()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Pocket Ask' })).toBeHidden()
  await expect(launch).toBeFocused()
})
