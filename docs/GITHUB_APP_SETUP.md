# Setting up the GitHub App for `srm`

`srm` authenticates as a **GitHub App installation** - not a personal access
token. Installation tokens auto-refresh (~1 hour), are scoped to exactly the
permissions you grant, and get higher API rate limits. This is the **only**
supported auth mode.

You create **one** App and **install** it on each organization you want to
manage. Every installation has its own **Installation ID**; the **App ID** and
private key are shared across installations.

---

## 1. Create the App

As an org owner: **Org Settings → Developer settings → GitHub Apps → New GitHub App.**
(You may also create it under a personal account - it can still be installed on
organizations.)

`srm` authenticates as an **installation**, never on behalf of a user, so every
user-facing section of the form (OAuth, device flow, setup URL, webhook) is left
**off**. Fill the form top to bottom:

- **GitHub App name:** e.g. `acme-srm`.
- **Homepage URL:** anything (e.g. your repo URL).
- **Identifying and authorizing users:** leave **Callback URL** blank and leave
  **Expire user authorization tokens**, **Request user authorization (OAuth)
  during installation**, and **Enable Device Flow** all **unchecked**.
- **Post installation:** leave **Setup URL** blank and **Redirect on update**
  unchecked.
- **Webhook:** **uncheck “Active”** and leave **Webhook URL** / **Secret** blank.
  `srm` polls the API; it does not receive webhooks.
- **Where can this GitHub App be installed:** **“Only on this account”** to lock
  it to a single org (e.g. `@Acme`), or **“Any account”** if you’ll install
  it across several orgs you own.

## 2. Permissions

`srm` manages runners at the **organization** level, so the only permissions it
needs live under **Organization permissions**. Leave **Repository permissions**
and **Account permissions** at their defaults.

Under **Permissions → Organization permissions**:

**Required**
- **Self-hosted runners: Read and write** - list/create/delete runners, mint
  registration & JIT tokens, and manage runner groups and labels.

**Optional** (only for the artifact/log **retention** panel and the retention
line in `srm doctor`)
- **Administration: Read and write** - gates the org-level artifact-and-log
  retention API. Without it, runner management works normally and retention
  simply shows “unavailable”.

**Do not** select any **Repository permissions** - `srm` makes no
repository-level calls. In particular the repository **Artifact metadata**
permission is *not* what drives retention (that is org **Administration**,
above), so leave it unselected. **Metadata: Read** shows as **Mandatory** on the
form; GitHub grants it automatically and it cannot be removed. No account
permissions or webhook event subscriptions are required.

## 3. Generate a private key

In the App’s settings → **Private keys → Generate a private key**. A `.pem`
downloads. Put it on the server with strict permissions, e.g.:

```bash
sudo install -d -m 0700 /etc/srm
sudo install -m 0600 ~/Downloads/acme-srm.*.private-key.pem /etc/srm/acme.pem
```

`srm init` can optionally **encrypt this key into srm’s secrets file** so the
`.pem` doesn’t need to stay on disk (see *Secrets* below).

## 4. Install the App on your org

App settings → **Install App → choose the org → Install.** Granting “All
repositories” is fine - runner management is org-level, so repository selection
does not affect runner operations, but an installation must exist.

## 5. Collect the three values

| Value | Where to find it |
| --- | --- |
| **App ID** | App settings → *About* (“App ID: 123456”). |
| **Installation ID** | After installing: **Org Settings → GitHub Apps → Configure** the app - the page URL ends with `…/installations/7654321`. (Or `gh api /orgs/<org>/installation -q .id`.) |
| **Private key path** | Where you saved the `.pem` (e.g. `/etc/srm/acme.pem`). |

## 6. Configure `srm`

Interactive (recommended) - run once per org:

```bash
srm init
```

It prompts for the org slug, App ID, installation ID, private key path, default
group/labels, and install root, then writes the config file. The default target
is **`/etc/srm/config.yaml`** when run as root (the standard for this
sudo-operated tool); otherwise the per-user `~/.config/srm/config.yaml`. Pass
`--config <path>` to override. Then verify:

```bash
srm doctor
```

---

## Full config reference

```yaml
concurrency: 8             # bulk-op fan-out, kept under GitHub's 100-concurrent limit
runnerVersion: "2.335.1"   # pinned actions/runner release the orchestrator installs (v1)
logFile: ""                # optional log file path; empty = stderr
dryRun: false              # preview mutations without calling GitHub (also --dry-run)

resources:                 # per-runner cgroup limits (systemd, opt-in); omit = unlimited
  memoryHigh: 2G           # soft cap: throttle + reclaim above this
  memoryMax: 3G            # hard cap: the job's cgroup is OOM-killed above this
  memorySwapMax: "0"       # no swap for the cgroup → host survives memory pressure
  cpuWeight: "100"         # relative CPU share under contention (1..10000)
  tasksMax: "4096"         # max processes/threads in the unit

isolation:                 # cross-org isolation (opt-in); omit = shared single user
  perOrgUsers: true        # each org runs as srm-<org> with private HOME + caches (0700)

orgs:
  - name: acme                         # the org login/slug
    appID: 123456                      # GitHub App ID
    installationID: 7654321            # THIS org's installation ID
    privateKeyPath: /etc/srm/acme.pem  # path to the App .pem (omit if stored in secrets)
    defaultGroupID: 1                  # 1 = the org's default runner group
    defaultLabels: [self-hosted, linux, x64]
    installRoot: /opt/actions-runners  # per-runner dirs live here (v1 orchestration)
    runnerUser: srm-acme               # optional per-org user (needs isolation.perOrgUsers)
    resources: { memoryMax: 6G }       # optional: field-level override of host defaults
    profiles:                          # optional runner profiles (v1)
      - name: build-heavy
        labels: [build, docker]
        groupID: 1
        ephemeral: true                # JIT, one job then auto-deregister
        requireJobContainer: false     # false = jobs use the host-baked toolchain
        manifest:
          aptPackages: [build-essential, git, jq]
          setupScripts: [/etc/srm/setup/install-node.sh]
          toolCacheSeeds: []
          persistentCachePaths: [/opt/hostedtoolcache]
```

| Field | Meaning |
| --- | --- |
| `concurrency` | Max parallel API calls for bulk ops (keep < 100). |
| `runnerVersion` | actions/runner release the orchestrator installs (v1). |
| `resources` | Host-wide per-runner cgroup limits (opt-in; omit = unlimited). Apply changes with `srm runners refresh`. |
| `isolation.perOrgUsers` | Run each org as its own user (`srm-<org>`) with private HOME + caches (opt-in). Migrate with `srm runners refresh --org <org>`. |
| `orgs[].name` | Org login/slug. |
| `orgs[].appID` | GitHub App ID (shared across installations). |
| `orgs[].installationID` | The installation ID for **this** org. |
| `orgs[].privateKeyPath` | Path to the App `.pem`; omit if importing into secrets. |
| `orgs[].defaultGroupID` | Runner group new runners join (1 = default group). |
| `orgs[].defaultLabels` | Labels applied to new runners. |
| `orgs[].installRoot` | Host dir for per-runner folders (v1). |
| `orgs[].runnerUser` | Per-org service user (default `srm-<org>`); only with `isolation.perOrgUsers`. |
| `orgs[].resources` | Per-org cgroup limits; overrides the host `resources` field by field. |
| `orgs[].profiles` | Reusable runner profiles + host dependency manifests (v1). |

## Secrets

The App private key can either live as a `.pem` referenced by `privateKeyPath`,
or be stored **encrypted** in srm’s secrets file:

- Export `SRM_SECRETS_PASSPHRASE` in the environment.
- During `srm init`, accept “encrypt the private key” - it stores the key under
  `app_key:<org>` in `secrets.age` next to the active config (e.g.
  `/etc/srm/secrets.age`, age, mode 0600) and clears `privateKeyPath`.
- At runtime `srm` resolves the key from `privateKeyPath` first, then from the
  secrets store key `app_key:<org>`.

Short-lived registration/remove tokens (~1 h) are minted on demand and never
stored.

## Multiple organizations

Install the same App on each org (each gets its own installation ID), then run
`srm init` once per org (or add one `orgs:` entry per org). Switch between them
in the TUI with `o`, or scope a command with `--org <slug>`.
