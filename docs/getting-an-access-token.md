# Getting a Cognito access token

Every route except `/healthz` needs a Cognito **access** token in the
`X-User-Token` header. This service cannot give you one: it only ever verifies
tokens against the User Pool's public JWKS. Nothing here holds a credential
that could create or authenticate a user — archives-ui talks to Cognito, and
this API never does.

So getting a token means going to Cognito (or to the UI, which already has).
Three ways, easiest first.

## 1. Borrow the one the UI already has

Log into archives-ui, then open devtools: **Application → Cookies →
`archive_access_token`**. Copy the value.

This is the least work and uses a path that's already known to work. See
[Is it safe to read it out of the cookie?](#is-it-safe-to-read-it-out-of-the-cookie)
below, since the answer isn't quite "yes" or "no".

## 2. `make token`

```bash
export TOKEN_USER=smoketest@example.com
export TOKEN_PASSWORD='...'
make token
```

Prints just the access token, so it pipes cleanly:

```bash
curl -s -H "X-API-Key: dev-key" -H "X-User-Token: $(make -s token)" \
  http://localhost:8080/v1/me
```

It reads `COGNITO_USER_POOL_ID` and `COGNITO_APP_CLIENT_ID` from your
environment — `set -a; source .env; set +a` if they aren't already exported.

The underlying call is `admin-initiate-auth` with the
`ADMIN_USER_PASSWORD_AUTH` flow, which is enabled on the app client. It needs
AWS credentials with `cognito-idp:AdminInitiateAuth`, so it works from a
machine with `aws configure` set up — not from CI unless you give CI that
permission.

## 3. The UI's own login route

If archives-ui is running locally, its login route does the Cognito call for
you and hands back cookies:

```bash
curl -i -s -X POST http://localhost:3000/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"smoketest@example.com","password":"..."}' \
  | grep -i '^set-cookie: archive_access_token'
```

Useful when you want to exercise the same path the browser takes, including
the refresh-and-retry behaviour, rather than going straight to Cognito.

## Making a test user

If there's no confirmed user to log in as yet:

```bash
aws cognito-idp admin-create-user --user-pool-id $COGNITO_USER_POOL_ID \
  --username someone@example.com --temporary-password 'Temp1234!' \
  --message-action SUPPRESS

aws cognito-idp admin-set-user-password --user-pool-id $COGNITO_USER_POOL_ID \
  --username someone@example.com --password 'Temp1234!' --permanent
```

`--message-action SUPPRESS` stops Cognito emailing the address, and
`--permanent` skips the forced password change that would otherwise block the
first login.

Local development points at the same real User Pool production uses — there is
no Cognito emulator, and one pool is simpler than keeping two in sync — so use
a throwaway address rather than your own.

## Two ways this goes wrong

**An ID token is rejected.** Take `AuthenticationResult.AccessToken`, not
`IdToken`. The verifier requires the `token_use` claim to be `access` and
refuses ID tokens outright, so a confident-looking JWT can still come back
`401`. This is deliberate: an ID token is a statement about who someone is,
meant for the client that requested it, not a credential for calling an API.

**Tokens last 12 hours.** A run of `401`s that starts suddenly, on requests
that worked earlier, almost always means the token expired rather than
anything being misconfigured. Get a new one before debugging anything else.

The response body is identical either way — `{"error":{"code":
"unauthenticated"}}` — because telling a caller *why* their token failed tells
an attacker the same thing.

## Is it safe to read it out of the cookie?

The cookie is set `httpOnly`, `sameSite: lax`, and `secure` in production
(`lib/session.ts`). That's the right configuration, and worth separating into
two questions.

**Does devtools showing it mean something is wrong?** No. `httpOnly` stops
*JavaScript on the page* from reading the cookie — an injected script can't do
`document.cookie` and exfiltrate it. It was never meant to hide the value from
the browser's own UI. You can always see your own cookies in your own
devtools, and an attacker doesn't have your devtools. Being able to read it
this way is not a hole.

What `httpOnly` buys, precisely: script injected into the page can still
*make requests* that carry the cookie, but it cannot steal the token and use
it somewhere else later. That's a meaningful reduction, not immunity, and it's
why a cookie beats `localStorage` for this.

**Is it safe to carry the token around once you've copied it?** Treat it as a
password for 12 hours. It's a bearer token: whoever holds it can act as that
user until it expires, and nothing about it is tied to your machine. So:

- Don't commit it. Bruno stores secret environment variables outside the
  `.bru` files for this reason.
- Don't paste it into a ticket, a chat, or a screenshot.
- Prefer a throwaway test user over your own account.
- **Leave the refresh token alone.** `archive_refresh_token` lasts 30 days and
  can mint new access tokens, so it's the more valuable of the two. Nothing in
  testing needs it.

The 12-hour expiry is what keeps the consequences small: a leaked access token
is a problem that fixes itself, which is not true of the refresh token or of
the API key.
