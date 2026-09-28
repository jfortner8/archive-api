# Bruno collection

Open the `bruno` folder in [Bruno](https://usebruno.com) (**Open Collection**,
not import). Bruno keeps a collection as `.bru` files on disk rather than in a
cloud workspace, which is the reason to use it here: these requests are
reviewed and versioned alongside the spec they exercise, and they break in a
pull request when the API changes.

## Running it

1. Pick the **local** environment.
2. `make dev-up && make dev-setup && make run`
3. Set `userToken` (see below).
4. Run the folders in order, or use the collection runner.

The requests are a flow, not a pile. Each stores the ids the next one needs as
runtime variables, so nothing has to be pasted between them: `get me` captures
the archive id, `create photo` captures the item id and its ETag, `request
upload url` captures the file id and the presigned URL.

Folders are numbered because that order matters — `06-tree` needs the people
`05-subjects` created.

## The one thing you have to fetch yourself

`userToken` is a Cognito **access** token, and this API cannot give you one: it
only ever verifies tokens. Nothing here holds a credential that could create or
authenticate a user — the frontend talks to Cognito, and this service never
does.

```bash
aws cognito-idp admin-initiate-auth \
  --user-pool-id $COGNITO_USER_POOL_ID \
  --client-id $COGNITO_APP_CLIENT_ID \
  --auth-flow ADMIN_USER_PASSWORD_AUTH \
  --auth-parameters USERNAME=test@example.com,PASSWORD='Temp1234!'
```

Copy `AuthenticationResult.AccessToken` into the environment's `userToken`. It
is declared secret, so it stays out of the committed files.

Two ways this goes wrong: tokens last 12 hours, so a sudden run of `401`s
usually means yours expired. And an **ID** token is rejected outright — the
verifier requires `token_use` to be `access`.

## Requests that are meant to fail

Seven of them assert a rejection rather than a success, because the rules are
the interesting part:

| Request | Asserts |
| --- | --- |
| `patch with stale etag` | `412` — someone else's edit is not silently overwritten |
| `reject an unknown field` | `400`, naming the misspelled field |
| `reject a wrong slot` | `422` — before an upload URL is issued, not after the bytes arrive |
| `reject wrong media` | `422` — audio cannot go in an image slot |
| `reject an unknown attribute` | `422` — a pet has no `favouriteSnack` |
| `reject a person parenting a dog` | `422` — people and animals cannot share descendants |
| `reject a cycle` | `422` — nobody can be their own ancestor |

## The production environment

Production sits behind a Lambda Function URL with `AWS_IAM` auth, so requests
need a SigV4 signature as well as the two headers. The environment wires
Bruno's own AWS auth for that; fill in credentials for the IAM user allowed to
invoke the function, and set `baseUrl` to the Function URL.

Without those you get a bare `Forbidden` from AWS before any of this API's code
runs — which looks nothing like this API's own error envelope, and that is how
you tell the two apart.
