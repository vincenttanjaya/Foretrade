// The operator journey: overview → users → create a user (password shown once) → every detail action → audit log.
import { expect, httpError, test, type Page } from './fixtures'

const dialog = (page: Page, name: string | RegExp) => page.getByRole('dialog', { name })

/** Opens a confirmation from its page button and confirms it with the dialog's own button. */
async function confirm(page: Page, button: string, title: string | RegExp, confirmLabel: string) {
  await page.getByRole('button', { name: button, exact: true }).click()
  await dialog(page, title).getByRole('button', { name: confirmLabel }).click()
  await expect(dialog(page, title)).toBeHidden()
}

test.describe('journey', () => {
  test.use({ allowedErrors: [httpError(404)] }) // the deleted account's page, at the end

  test('overview → create a user → manage them → audit', { tag: '@mobile' }, async ({ page, context, isMobile }) => {
    if (!isMobile) await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await page.goto('/')
    await expect(page.getByRole('heading', { level: 1, name: 'Platform overview.' })).toBeVisible()
    const kpis = page.getByRole('region', { name: 'Key numbers' })
    await expect(kpis).toContainText('Users31')
    await expect(page.getByRole('group', { name: 'AI credits per day, last 30 days' }).getByRole('img')).toHaveCount(30)
    await expect(page.getByRole('heading', { name: 'System status' })).toBeVisible()
    await expect(page.getByText('Unreachable')).toBeVisible() // the mock's crawler

    // Users: search, then create.
    await page.getByRole('link', { name: 'Users', exact: true }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Users.' })).toBeVisible()
    await expect(page.getByText('31 accounts')).toBeVisible()
    await page.getByLabel('Search').fill('ava')
    await expect(page).toHaveURL('/users?q=ava')
    await expect(page.getByRole('link', { name: 'Ava Tan' })).toBeVisible()
    await expect(page.getByText('1 matching account')).toBeVisible()
    await page.getByLabel('Search').fill('')

    await page.getByRole('button', { name: 'New user' }).click()
    const create = dialog(page, 'Create a user.')
    await create.getByLabel('Email').fill('ann.lee@example.com')
    await create.getByLabel('Name').fill('Ann Lee')
    await create.getByRole('button', { name: 'Create user' }).click()

    const reveal = dialog(page, 'Account created.')
    await expect(reveal).toContainText('Shown once.')
    const password = await reveal.getByLabel('Temporary password').inputValue()
    expect(password).toMatch(/^[A-Z2-7]{26}$/)
    if (!isMobile) {
      await reveal.getByRole('button', { name: 'Copy' }).click()
      await expect(reveal.getByRole('button', { name: 'Copied' })).toBeVisible()
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(password)
    }
    await page.keyboard.press('Escape') // can't be dismissed by accident
    await expect(reveal).toBeVisible()
    await reveal.getByRole('button', { name: "I've saved it" }).click()

    // Detail: the new account.
    await expect(page.getByRole('heading', { level: 1, name: 'Ann Lee' })).toBeVisible()
    await expect(page).toHaveURL(/\/users\/[0-9a-f-]{36}$/)
    expect(page.url()).not.toContain(password)
    expect(await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }))).not.toContain(
      password,
    )
    await expect(page.getByText(password)).toHaveCount(0)

    await page.getByRole('button', { name: 'Edit profile' }).click()
    const edit = dialog(page, 'Edit profile')
    await edit.getByRole('checkbox', { name: /Use default/ }).uncheck()
    await edit.getByLabel('Custom daily credit limit').fill('50')
    await edit.getByRole('button', { name: 'Save changes' }).click()
    await expect(page.getByText('Saved ann.lee@example.com.')).toBeVisible()
    await expect(page.getByText('Daily limit').locator('..')).toContainText('50Custom')

    await confirm(page, 'Disable account', 'Disable Ann Lee?', 'Disable account')
    await expect(page.getByText('Disabled ann.lee@example.com.')).toBeVisible()
    await page.getByRole('button', { name: 'Enable account' }).click()
    await expect(page.getByText('Enabled ann.lee@example.com. They can sign in again.')).toBeVisible()

    await page.getByRole('button', { name: 'Set password', exact: true }).click()
    await dialog(page, 'Set a password').getByRole('button', { name: 'Generate password' }).click()
    const second = await dialog(page, 'New password set.').getByLabel('Temporary password').inputValue()
    expect(second).not.toBe(password)
    await dialog(page, 'New password set.').getByRole('button', { name: "I've saved it" }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)

    await confirm(page, 'Revoke sessions', 'Revoke all sessions?', 'Revoke sessions')
    await expect(page.getByText('Revoked 0 sessions.')).toBeVisible()
    await confirm(page, "Reset today's credits", "Reset today's AI credits?", 'Reset credits')
    await confirm(page, 'Reset portfolio', 'Reset the paper portfolio?', 'Reset portfolio')
    await expect(page.getByText('Reset the paper portfolio of ann.lee@example.com.')).toBeVisible()

    const audit = page
      .getByRole('heading', { name: 'Audit', exact: true })
      .locator('xpath=ancestor::div[contains(@class,"card")][1]')
    await expect(audit.getByRole('listitem')).toHaveText([
      /^Reset portfolio/,
      /^Reset today's credits/,
      /^Revoked sessions/,
      /^Generated password/,
      /^Enabled user/,
      /^Disabled user/,
      /^Updated usercredit limit: default → 50/,
      /^Created user/,
    ])

    // Delete: only after typing the exact email.
    const del = page.getByRole('button', { name: 'Delete account' })
    await expect(del).toBeDisabled()
    await page.getByLabel(/to confirm/).fill('ann.lee@example')
    await expect(del).toBeDisabled()
    await page.getByLabel(/to confirm/).fill('ann.lee@example.com')
    await del.click()
    await expect(page).toHaveURL('/users')
    await expect(page.getByText('Deleted ann.lee@example.com.')).toBeVisible()
    await expect(page.getByText('31 accounts')).toBeVisible()

    // The audit log outlives the account.
    await page.getByRole('link', { name: 'Audit', exact: true }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Every admin change.' })).toBeVisible()
    const rows = page.getByRole('table').getByRole('row')
    await expect(rows).toHaveCount(10) // header + 9 entries
    await expect(rows.nth(1)).toContainText('Deleted user')
    await expect(rows.nth(9)).toContainText('Created user')
    await expect(page.getByRole('table')).not.toContainText(password)
    await rows.nth(1).getByRole('link', { name: 'ann.lee@example.com' }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Account deleted' })).toBeVisible()
  })
})

test('safety rules: your own account and managed passwords', async ({ page }) => {
  await page.goto('/users?role=admin')
  await page.getByRole('link', { name: 'Admin', exact: true }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Admin' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Disable account' })).toBeDisabled()
  await expect(page.getByText("You can't disable your own account.")).toBeVisible()
  await expect(page.getByText("You can't delete your own account.")).toBeVisible()
  await expect(page.getByRole('button', { name: 'Set password' })).toBeDisabled() // managed (bootstrap) account
  await page.getByRole('button', { name: 'Edit profile' }).click()
  await expect(dialog(page, 'Edit profile').getByLabel('Role')).toBeDisabled()
})

test.describe('conflicts', () => {
  test.use({ allowedErrors: [httpError(409)] })

  test('a server-side 409 is shown verbatim', async ({ page }) => {
    await page.goto('/users')
    await page.getByRole('button', { name: 'New user' }).click()
    const create = dialog(page, 'Create a user.')
    await create.getByLabel('Email').fill('demo@example.com')
    await create.getByLabel('Name').fill('Duplicate')
    await create.getByRole('button', { name: 'Create user' }).click()
    await expect(create.getByRole('alert')).toHaveText('An account with this email already exists.')
  })
})
