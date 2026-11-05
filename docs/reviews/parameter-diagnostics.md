# 型付きパラメータの診断位置

## 問題

`--parameter-file`のJSON値はMCLの型検査まで渡りますが、ファイルパスとJSON内の位置はRuntimeValueへ引き継がれませんでした。そのため、`settings.envoyMemory`へ数値を渡した場合でも、CLIは修正対象のJSON値ではなく、MCLの`param settings`宣言を指していました。

## 採用した設計

CLIは各パラメータの値と入力元情報を分けて保持します。`EvaluationOptions.ParameterSources`はパラメータ名ごとのラベル、ファイルパス、RFC 6901 JSON Pointerから値とオブジェクトキーのバイト範囲への位置表を持ちます。既存の値のみを受け取る評価APIはそのまま使用でき、新しい入力元付きAPI群は追加しません。

JSON位置表は`encoding/json/jsontext`のDecoderで一度だけ生成します。JSONの二重パーサーや、フィールドごとにファイルを再走査する処理はありません。CLIの`render`、`check`、`explain`、`diff`、`update`は同じ入力元情報をEvaluationOptionsへ設定して評価します。

JSON値からRuntimeValueを作るとき、各値にファイルパスと値のバイト範囲を、各Map項目にキーのバイト範囲を設定します。型検査の失敗は期待型、実際の型、失敗したフィールドまたはList indexを構造化して保持します。型検査メッセージの文字列を解析してパスを再構築しません。

## 診断例

次のJSONを`deployment-parameters.json`として読み込み、`settings.envoyMemory`が`String`型であるMCLへ渡すと、`64`の位置を主診断にします。

```json
{
  "settings": {
    "envoyMemory": 64
  }
}
```

```text
deployment-parameters.json:3:20: MCL-TYPE: parameter "settings" does not match its declared type Settings
path: settings.envoyMemory
pointer: /settings/envoyMemory
expected: String
actual: Int
```

`param settings`の宣言位置はRelated locationとして表示します。パラメータ診断は入力ファイルと宣言ファイルの行・列を表示しますが、両方のソース行と値の抜粋を表示しません。パラメータの型不一致診断は指定値そのものを含めず、期待型と実際の型で説明します。

人が読むパスはレコード項目にドット記法、List要素に`[index]`、記号を含むMap keyに引用符付きの角括弧記法を使います。機械的な照合にはRFC 6901 JSON Pointerを使い、`~`と`/`はそれぞれ`~0`と`~1`へ変換します。

## 入力元がない場合

既存ライブラリAPIから位置情報なしで値を渡した場合も、診断には失敗したJSONパス、期待型、実際の型を含め、MCLの`param`宣言を主診断位置にします。`--parameter name=...`は`--parameter name`を入力元として示し、ファイルの行・列を生成しません。省略必須パラメータと既定値の型エラーはMCL宣言を位置の正本とします。

ArgoCDのCMPが複数の設定を一時JSONへ集約した場合、CLIが示すファイルと行・列はその一時JSONの位置です。元の`app.json`までのsource mapやプラグイン用の診断プロトコルは追加しません。

## Kubernetes検証の責務

組み込みKubernetesリソースの`metadata.name`へ`ValidateObjectMeta()`による意味検証を追加しません。MCLの組み込みリソース検査はJSON Schemaに記載された型、制約、未知フィールドを検査し、Kubernetesの命名規則やAdmission処理を再実装しません。

既存の`internal/engine/crd.go`には、出力するCustom Resourceインスタンスの`metadata`へ`ValidateObjectMeta()`を適用する処理があります。これはCRD定義Schemaの構築・妥当性検査とは別の検証です。今回の方針確認を理由にCRD処理全体を削除せず、両者を区別してレビューしました。

## 検証

`go test ./internal/engine ./cmd/mcl`を実行し、型不一致の入れ子パスとJSON位置、List/Mapの失敗パス、missing field、Union、JSON Pointerのエスケープ、過剰なJSON深さ、CLIの`check --parameter-file`を確認しました。`go test ./...`と`golangci-lint run ./...`も成功しました。
JSONのソース行を表示せずに位置を示すCLIの回帰テストと、入れ子の未知フィールドのJSONキー位置を示す回帰テストも通過しました。

## 公開バージョン

[MCL v0.3.31](https://github.com/manifest-configuration-language/mcl/releases/tag/v0.3.31)で公開済みです。
[Release workflow](https://github.com/manifest-configuration-language/mcl/actions/runs/42042596660)のCLI、WASM、attestation、GitHub Release、npm公開がすべて成功しました。
