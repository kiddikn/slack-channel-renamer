# slack-channel-renamer

Bulk rename Slack public channels using a CSV mapping file.

## Setup

### 1. Create a Slack App

1. Go to https://api.slack.com/apps and click **Create New App**
2. Choose **From scratch**, give it a name, and select your workspace

### 2. Add User Token Scopes

In your app settings, navigate to **OAuth & Permissions** and add the following **User Token Scopes**:

| Scope             | Purpose                |
|-------------------|------------------------|
| `channels:write`  | Rename public channels |
| `channels:read`   | List public channels   |

> **Note**: `conversations.rename` requires a User Token (`xoxp-`). Bot Tokens (`xoxb-`) will return `not_authorized` regardless of scopes.

### 3. Install App to Workspace

On the **OAuth & Permissions** page, click **Install to Workspace** and authorize the app.

### 4. Copy the User Token

After installation, copy the **User OAuth Token** (starts with `xoxp-`).

### 5. Export the Token

```bash
export SLACK_USER_TOKEN=xoxp-your-token-here
```

### 6. Prepare the CSV file

Create `channel_mapping.csv` in the same directory as `main.go`:

```csv
asis,tobe
old-channel-1,new-channel-1
old-channel-2,new-channel-2
```

- `asis`: current channel name (must exist as a public, non-archived channel)
- `tobe`: desired new name

### 7. Run

```bash
go run main.go
```

## Example output

```
12:34:56 loaded 2 rename entries from channel_mapping.csv
12:34:56 fetched 42 public channels
12:34:56 validation passed, starting rename...
OK: old-channel-1 -> new-channel-1
OK: old-channel-2 -> new-channel-2
```

If validation fails, no renames are executed:

```
validation errors:
  - channel "old-channel-99" does not exist
  - channel name "New Channel!" is invalid (must match ^[a-z0-9_-]{1,80}$)
```

## Naming rules

Slack channel names must:

- Contain only lowercase letters (`a-z`), numbers (`0-9`), hyphens (`-`), or underscores (`_`)
- Be between 1 and 80 characters long
- Not start or end with a hyphen (Slack enforces this server-side)

## Rate limiting

The Slack API enforces rate limits on `conversations.rename` (Tier 2: ~20 requests/minute).
This tool:

- Sleeps 1 second between each rename call
- Automatically retries up to 3 times when a rate-limit error is received, waiting the duration indicated by the API response

## Notes

- Only **public** channels are processed; private channels are ignored
- **Archived** channels are excluded from the channel list and cannot be renamed
- Validation runs before any rename is attempted — either all renames proceed or none do
- Exit code is `0` only when all renames succeed; any failure returns a non-zero exit code

## Future improvements

- **Dry run mode**: add a `--dry-run` flag to print what would be renamed without calling the API
- **Private channel support**: add `private_channel` to the types list with the `groups:write` scope
- **Concurrency**: process renames in parallel with a configurable worker pool and shared rate-limit budget
- **CSV output**: write a results CSV with OK/FAIL status for audit purposes

## Security

**Never commit your Slack token to version control.**

Store it in an environment variable, a secrets manager (e.g., AWS Secrets Manager, HashiCorp Vault), or a `.env` file that is listed in `.gitignore`.
