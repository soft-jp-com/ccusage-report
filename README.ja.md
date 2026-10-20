# ccusage-report

[English](README.md) | 日本語

PC に残っている **Claude Code** のセッションログを集計し、トークン使用量と API 換算コストを **Markdown のレポート**にまとめる Go 製のコマンドラインツールです。実際の使い方に **Pro・Max5・Max20** のどのプランが合っているかの目安も出します。

集計方法は [ccusage](https://github.com/ryoppippi/ccusage) と同じですが、実行ファイル 1 つで動きます。Node.js・npx・Python は要りません。

## 特徴

- **使用量レポート**：期間合計・日次の使用量と、モデル別の内訳（入力、出力、キャッシュ作成、キャッシュ読込、合計トークン、コスト）
- **プラン判定**：使用量を 5 時間ブロックに分けて Pro / Max5 / Max20 の上限の目安と比べ、上げる・下げる・そのままのどれがよいかを出す
- **リミット到達履歴**：Claude Code がリミット（5 時間枠・週枠）で止まった日時と、その枠内で使った API 換算コスト（＝そのプランの上限の実測値）
- **依存なし**：`go run` で実行するか、ビルドした実行ファイル 1 つを各 PC にコピーするだけで使える
- **日本語 / 英語**：OS の表示言語に合わせる。`-lang` で指定もできる

## 必要なもの

- PC 上の Claude Code のセッションログ（`~/.claude/projects/`）
- Go 1.22 以上

主な対象は Windows です。macOS と Linux でもビルドできます。

## Go で実行する

実行ファイルは配布していません。`go` コマンドで実行するか、ビルドして使ってください。

```bash
git clone https://github.com/soft-jp-com/ccusage-report.git
cd ccusage-report

# ビルドせずに実行（レポートはカレントフォルダに出力）
go run .
go run . -since 20260920 -until 20261020
```

`$GOPATH/bin`（または `GOBIN`）にインストールすることもできます。

```bash
go install github.com/soft-jp-com/ccusage-report@latest
ccusage-report -out ./report.md
```

## ビルド

```bash
# Windows
go build -o ccusage-report.exe .

# macOS / Linux
go build -o ccusage-report .
```

`GOOS` と `GOARCH` を指定すると、別の OS 向けにビルドできます。

```bash
# Windows 向け（macOS / Linux から）
GOOS=windows GOARCH=amd64 go build -o ccusage-report.exe .

# macOS（Apple シリコン）向け
GOOS=darwin GOARCH=arm64 go build -o ccusage-report .

# Linux 向け
GOOS=linux GOARCH=amd64 go build -o ccusage-report .
```

PowerShell では先に環境変数を設定します：`$env:GOOS="darwin"; $env:GOARCH="arm64"; go build -o ccusage-report .`

ビルドした実行ファイルは他に何も必要としないので、そのまま他の PC にコピーして使えます。

## 使い方

```bash
# ログに残っている全期間を集計（実行ファイルと同じフォルダに出力）
./ccusage-report

# 期間を指定
./ccusage-report -since 20260920 -until 20261020

# 英語で標準出力に出す
./ccusage-report -lang en -out -
```

Windows では `ccusage-report.exe` を実行します。`go run` の場合は、`go run .` の後ろにオプションを付けます（例：`go run . -lang en -out -`）。

レポートは、実行ファイルと同じフォルダに `claude_usage_<ユーザーID>_<YYYYMMDD>.md` という名前で出力されます。`<ユーザーID>` はログインしているユーザー名です。`go run` で実行した場合はカレントフォルダに出力されます。`go install` で入れた場合、実行ファイルは `$GOPATH/bin`（または `GOBIN`）に置かれるので、別の場所に出力したいときは `-out` を指定してください。

### オプション

| オプション | 既定値 | 説明 |
|---|---|---|
| `-since` | | 集計開始日（`YYYYMMDD` または `YYYY-MM-DD`） |
| `-until` | | 集計終了日（当日を含む） |
| `-out` | 実行ファイルと同じフォルダ | 出力先ファイル。`-` で標準出力 |
| `-lang` | OS の表示言語 | レポートの言語（`ja` / `en`） |
| `-timezone` | PC の設定 | 日付の区切りに使うタイムゾーン（例：`Asia/Tokyo`） |
| `-offline` | オフ | 料金表をダウンロードせず、キャッシュか組み込みの料金表を使う |
| `-max5-limit` | `155` | Max5 の 5 時間枠上限の目安（API 換算 USD）。Pro はこの 1/5、Max20 は 4 倍とする |
| `-near` | `0.7` | プランの上限のこの割合に達したら「限界近く」とみなす |
| `-freq` | `1` | 「限界近く」が週にこの回数以上なら、そのプランでは足りないとみなす |
| `-no-plan` | オフ | プラン判定を出力しない |

## レポートの内容

1. **期間合計**：利用日数、トークン数、API 換算コスト、1 日あたり平均、現在のプランと推奨プラン
2. **期間合計・モデル別**
3. **日次**と**日次・モデル別**
4. **プラン判定（目安）**
   - 推奨プランとその理由
   - 5 時間ブロックの使用量：中央値、上位 10%、最大と、各プランの上限に対する割合
   - 各プランの上限の 70% 以上に達したブロック数（週あたり）と、上限を超えたブロック数
   - 使用量の多いブロックの上位 5 件
   - リミット到達履歴と、それぞれの枠内で使ったコスト

## プラン判定の仕組み

Anthropic はサブスクリプションプランの利用上限を公開していません。このツールは **API 換算コストでの推定値**を使って判定します。

- **5 時間ブロック**：ccusage と同じです。最初のメッセージの時刻（時単位で切り捨て）から 5 時間を 1 ブロックとし、5 時間以上空いたら新しいブロックにします。
- **プランの上限**：既定の `-max5-limit 155` は、Max5 でリミットに達した 2 回の実測（5 時間枠あたり約 $150〜$158、2026 年 10 月）から決めた値です。Anthropic の「Pro の 5 倍 / 20 倍」という説明に合わせて、Pro は Max5 の 1/5、Max20 は 4 倍としています。
- **「限界近く」**：多くの人は実際にリミットに達する前に別の AI ツールに切り替えます。そこで、上限の **70%** に達した時点で「使い切った」とみなします。

| 判定 | 条件 |
|---|---|
| **上げる** | 現在のプランの上限の 70% 以上に**週 1 回以上**達している。または週枠のリミットに達した |
| **下げる** | 下位プランの上限を**一度も超えず**、その 70% 以上に達するのも週 1 回未満 |
| **そのまま** | どちらにも当てはまらない |

基準は `-near`・`-freq`・`-max5-limit` で調整できます。「リミット到達履歴」の表には、リミットに達した枠で実際に使ったコストが出ます。複数人の結果を集めて `-max5-limit` を調整してください。

## 精度と制約

- コストは **API 料金で換算した推定値**（USD）です。サブスクリプションの請求は月額の定額です。
- 料金は ccusage と同じ [LiteLLM](https://github.com/BerriAI/litellm) の料金表を使います。プロンプトの長さによる段階料金と、1 時間キャッシュの書き込み単価も反映しています。合計は ccusage と 0.1% 程度の差で一致します。
- 料金表をダウンロードできないときは、前回のキャッシュ、それもなければ組み込みの料金表（`prices_fallback.json`）を使います。
- claude.ai（Web・デスクトップ・モバイル）での利用も同じ上限を消費しますが、Claude Code のログには残らないため集計に含まれません。
- Claude Code はセッションログを既定で約 30 日で削除します（`cleanupPeriodDays`）。分析できるのは直近の利用だけです。
- 週枠は上限の大きさが分からないため、判定には使っていません。レポートには連続 7 日間の最大値だけを載せています。

## プライバシー

- 読むのは PC 上のファイルだけです。`~/.claude/projects/`（または `CLAUDE_CONFIG_DIR`）配下のセッションログと、プランを調べるための `~/.claude/.credentials.json` の `subscriptionType` / `rateLimitTier` です。トークンは読み込みも保持もしません。
- ネットワークにアクセスするのは、公開されている LiteLLM の料金表をダウンロードするときだけです。`-offline` で止められます。
- レポートにはユーザー名、使用量、モデル名が入ります。共有する前に内容を確認してください。

## 免責

非公式のツールです。Anthropic とは関係がなく、Anthropic が推奨するものでもありません。Claude Code のログ形式は予告なく変わることがあります。

## ライセンス

[Apache License 2.0](LICENSE)

組み込みの料金表（`prices_fallback.json`）は、MIT License の LiteLLM のデータから作っています。詳しくは [NOTICE](NOTICE) を参照してください。
