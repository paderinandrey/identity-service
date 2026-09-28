# Playbook: frontend e2e tests without the IdP form

Use it when a frontend Playwright suite needs signed-in users.

Frontend apps hold no auth state of their own — being "logged in" is exactly our session cookie. So Playwright suites do not have to drive the IdP login form: with `E2E_LOGIN_TOKEN` set (staging or local — startup fails if it is set with `APP_ENV=production`), `POST /internal/e2e/login` issues a regular session for an existing active user.

## Steps

```ts
// playwright global-setup.ts — once per role
// INTERNAL_URL points at the internal listener (INTERNAL_LISTEN_ADDR):
// the route does not exist on the public port, and the NetworkPolicy
// must list the test runner as an allowed peer.
const resp = await request.post(`${INTERNAL_URL}/internal/e2e/login`, {
  headers: { Authorization: `Bearer ${process.env.E2E_LOGIN_TOKEN}` },
  data: { email: 'qa@example.com' },
});
// persist the response cookies as storageState, then in tests:
//   test.use({ storageState: 'qa.json' })
```

The issued session is a normal one (rotation, limits, idle/absolute lifetimes) but deliberately leaves no sign-in traces: `last_sign_in_at` stays untouched and `sign_ins_total` does not count it, so test logins never distort real sign-in data. Unknown or deactivated users get 404.

## Common mistakes

- Cookies belong to the host that issued them. When the frontend and `/graphql` sit behind one entry proxy this is transparent; with split hosts, inject the cookie for the gateway domain and send frontend requests with `credentials: 'include'`.
- Keep one smoke test going through the real IdP form: the "deep link without a session → IdP → back to the same page" behaviour (our RelayState) is frontend-visible and programmatic login cannot cover it.
