# ccusage-report

English | [日本語](README.ja.md)

A command-line tool written in Go that reads your local **Claude Code** session logs and writes a **Markdown report** of token usage and API-equivalent cost. It also estimates whether **Pro, Max5 or Max20** fits how you actually use Claude Code.

It aggregates the logs the same way as [ccusage](https://github.com/ryoppippi/ccusage), but it is a single executable: no Node.js, npx or Python needed.

## Features

- **Usage report**: period total, daily usage and per-model breakdown (input, output, cache write, cache read, total tokens, cost)
- **Plan recommendation**: groups usage into 5-hour blocks and compares them with estimated limits for Pro / Max5 / Max20, then recommends upgrading, downgrading or staying
- **Limit hits**: lists every time Claude Code was rate-limited (5-hour or weekly) and the API-equivalent cost used in that window, which is the measured limit for your plan
- **No dependencies**: run it with `go run`, or build a single executable and copy it to each PC
- **Japanese / English**: follows the OS display language; override with `-lang`

## Requirements

- Claude Code session logs on the PC (`~/.claude/projects/`)
- Go 1.22 or later

Windows is the main target. The code also builds on macOS and Linux.

## Run with Go

No binaries are distributed. Run or build it with the `go` command.

```bash
git clone https://github.com/soft-jp-com/ccusage-report.git
cd ccusage-report

# Run without building (the report is written to the current folder)
go run .
go run . -since 20260920 -until 20261020
```

Or install it into `$GOPATH/bin` (or `GOBIN`):

```bash
go install github.com/soft-jp-com/ccusage-report@latest
ccusage-report -out ./report.md
```

## Build

```bash
# Windows
go build -o ccusage-report.exe .

# macOS / Linux
go build -o ccusage-report .
```

Build for another OS by setting `GOOS` and `GOARCH`:

```bash
# Windows (from macOS / Linux)
GOOS=windows GOARCH=amd64 go build -o ccusage-report.exe .

# macOS (Apple silicon)
GOOS=darwin GOARCH=arm64 go build -o ccusage-report .

# Linux
GOOS=linux GOARCH=amd64 go build -o ccusage-report .
```

In PowerShell, set the variables first: `$env:GOOS="darwin"; $env:GOARCH="arm64"; go build -o ccusage-report .`

The built executable has no dependencies, so you can copy it to other PCs.

## Usage

```bash
# Report everything in the logs (written next to the executable)
./ccusage-report

# Limit the period
./ccusage-report -since 20260920 -until 20261020

# Print to stdout in English
./ccusage-report -lang en -out -
```

On Windows, run `ccusage-report.exe` instead. With `go run`, put the options after `go run .` (for example `go run . -lang en -out -`).

By default the report is written next to the executable as `claude_usage_<user>_<YYYYMMDD>.md`, where `<user>` is the login user name. With `go run` it is written to the current folder. If you installed with `go install`, the executable is in `$GOPATH/bin` (or `GOBIN`), so pass `-out` to write the report somewhere else.

### Options

| Option | Default | Description |
|---|---|---|
| `-since` | | Start date (`YYYYMMDD` or `YYYY-MM-DD`) |
| `-until` | | End date, inclusive |
| `-out` | next to the executable | Output file; `-` for stdout |
| `-lang` | OS language | Report language: `ja` or `en` |
| `-timezone` | system | Time zone used to group by date (e.g. `America/New_York`) |
| `-offline` | off | Do not download the price table; use the cached or built-in one |
| `-max5-limit` | `155` | Estimated Max5 5-hour limit in API-equivalent USD. Pro is 1/5 of it, Max20 is 4x |
| `-near` | `0.7` | Fraction of a plan's limit treated as "near the limit" |
| `-freq` | `1` | "Near the limit" this many times per week or more means the plan is too small |
| `-no-plan` | off | Omit the plan recommendation |

## Report contents

1. **Period total**: active days, tokens, API-equivalent cost, average per day, current plan and recommended plan
2. **Period total by model**
3. **Daily** and **daily by model**
4. **Plan recommendation**
   - Recommendation and the reason
   - 5-hour block usage: median, top 10%, max, and the share of each plan's limit
   - Number of blocks at 70% or more of each plan's limit (per week) and over the limit
   - Heaviest 5 blocks
   - Limit hits and the cost used in each window

## How the plan recommendation works

Anthropic does not publish the usage limits of its subscription plans, so this tool works with **estimates in API-equivalent cost**.

- **5-hour blocks**: same as ccusage. A block starts at the first message (rounded down to the hour) and lasts 5 hours; a gap of 5 hours or more starts a new block.
- **Plan limits**: the default `-max5-limit 155` comes from two measured rate-limit hits on Max5 (about $150–$158 per 5-hour window, October 2026). Pro is assumed to be 1/5 and Max20 4x of Max5, following Anthropic's "5x / 20x Pro" description.
- **"Near the limit"**: many people switch to another AI tool before they actually hit the limit, so reaching **70%** of the limit already counts as using the plan up.

| Result | Condition |
|---|---|
| **Upgrade** | Blocks reach 70% or more of the current plan's limit **once a week or more**, or a weekly limit was hit |
| **Downgrade** | A lower plan's limit is **never exceeded**, and its 70% line is reached less than once a week |
| **Stay** | Neither of the above |

Tune the thresholds with `-near`, `-freq` and `-max5-limit`. The "Limit hits" table shows the cost actually used in each rate-limited window; collect it from several people to calibrate `-max5-limit`.

## Accuracy and limitations

- Costs are **API-equivalent estimates** (USD). A subscription is billed at a flat monthly price.
- Prices come from the [LiteLLM](https://github.com/BerriAI/litellm) price table, the same source ccusage uses. Prompt-size price tiers and the 1-hour cache write price are applied. Totals match ccusage within about 0.1%.
- If the price table cannot be downloaded, the tool uses the cached copy, then the built-in table (`prices_fallback.json`).
- Usage in claude.ai (web, desktop, mobile) counts against the same limits but is not in the Claude Code logs.
- Claude Code deletes session logs after about 30 days by default (`cleanupPeriodDays`), so only recent usage can be analyzed.
- Weekly limits are not used for the recommendation, because their size is unknown. The report only shows the largest 7-day total.

## Privacy

- The tool reads only local files: session logs under `~/.claude/projects/` (or `CLAUDE_CONFIG_DIR`), and `subscriptionType` / `rateLimitTier` in `~/.claude/.credentials.json` to find your plan. It does not read or keep tokens.
- The only network access is downloading the public LiteLLM price table. Turn it off with `-offline`.
- The report contains your user name, usage numbers and model names. Check it before you share it.

## Disclaimer

This is an unofficial tool. It is not affiliated with or endorsed by Anthropic. The log format of Claude Code may change without notice.

## License

[Apache License 2.0](LICENSE)

The built-in price table (`prices_fallback.json`) is derived from LiteLLM data under the MIT License. See [NOTICE](NOTICE).
