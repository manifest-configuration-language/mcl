# ArgoCDからの型付きパラメータ入力（実装済み）

## 問題

ArgoCD CMPからMCLへクラスタ情報、アプリのFeature、Settingsを渡すとき、値をコマンド文字列へ埋め込むとシェルの引用規則に依存します。文字列、配列、入れ子のオブジェクトを安全に渡すには、CMPがJSONファイルを生成し、MCLがファイルを直接読む入力経路が必要です。

## 実装

`mcl render`、`check`、`explain`、`diff`、`update`は、繰り返し指定できる`--parameter-file path.json`から実行時パラメータを読み込みます。各ファイルは重複キーを含まないトップレベルJSONオブジェクトである必要があり、オブジェクトのキーと値をパラメータ名と値として扱います。既存の`--parameter name=<JSON value>`も併用できます。同じパラメータ名を`--parameter`で複数回指定した場合、または複数ファイルと`--parameter`の間で指定した場合は、重複した名前と入力元を示して失敗します。

CMPは値をシェルコマンドへ補間せず、パラメータをJSONファイルへ書いてMCLへファイルパスを渡せます。JSON値はMCLの既存の型検査を通るため、Cluster factsとアプリ設定を型付きで検査できます。デプロイ先の列挙やApplicationSetの検証はこのCLI入力機能の責務に含めません。

## 使用例

```sh
mcl render apps/cilium/main.mcl \
  --parameter-file /work/cluster.json \
  --parameter-file /work/cilium-settings.json
```

各ファイルは、例えば`{"cluster": {"kubernetesVersion": "1.35"}}`のようにJSONオブジェクトを格納します。
