# MCL

MCLは、YAMLに近いインデント構文でKubernetes Manifestを組み立てる構成言語です。言語リファレンス、チュートリアル、Playgroundは[MCL Webサイト](https://mcl.walnuts.dev/)を参照してください。

## インストール

miseの`github:`バックエンドで、GitHub Releaseから最新の安定版をインストールします。

```sh
mise use -g github:manifest-configuration-language/mcl

mcl version
```

miseはGitHub Releaseの添付ファイルからOSとアーキテクチャに合うアーカイブを選択して展開します。`latest`はprereleaseを除外します。

CLIは`check`、`lint`、`fmt`、`render`、`update`、`fetch`、`lsp`、`completion`などのコマンドを提供します。使い方は`mcl help`で確認できます。

関数とResource Listの組み立て方、名前付き引数とデフォルト引数、Listの展開については[関数とList](docs/function-list-composition.md)を参照してください。

## Helm出力の再現性

`render`、`check`、`diff`、`explain`、`update`はHelm Chartが乱数関数を呼び出した場合に、同じ入力で2回レンダリングしたManifestをバイト単位で比較します。出力が異なる場合はテンプレート名を示して失敗し、出力が一致すれば最初の結果を使います。乱数関数を呼び出してもManifest出力が変わらないChartは通常どおり利用できます。

`render`、`check`、`explain`、`diff`、`update`には、`--parameter name=<JSON value>`または`--parameter-file path.json`で実行時パラメータを渡せます。パラメータファイルはトップレベルがJSONオブジェクトである必要があり、複数ファイルを指定できます。同じ名前を複数回指定するとエラーになります。

```sh
mcl render main.mcl --parameter-file cluster.json --parameter-file app-settings.json
```

Kubernetes出力の検証には、既定で組み込みの`v1.37.1` OpenAPI Schemaを使います。対象クラスタに合わせる場合は`--kubernetes-version v1.36.4`のように指定します。対応するKubernetes versionは[Project configuration](docs/project-configuration.md)に記載しています。

`mcl update`は依存のLockfileを更新します。既存の`mcl.lock`に固定された依存を取得するときは`mcl fetch --locked main.mcl`を実行してください。`fetch --locked`はLockfileを変更せず、取得した依存をcacheへ保存します。cacheが揃っていれば、通常の`mcl render`はネットワーク接続なしで実行できます。

再現性を必要としない場合は`--allow-nondeterministic-output`を指定できます。この場合は最初の出力を返し、その出力をレンダリングキャッシュへ保存しません。この検査は非決定的な関数が実行されたときの2回の結果を比較するもので、乱数による分岐などですべての実行の再現性を数学的に証明するものではありません。`lookup`は明示的なResource Snapshotを使い、`now`は固定時刻を指定した場合のみ使えます。DNS、環境変数、現在時刻への暗黙アクセスは引き続き無効です。

## 関連リポジトリ

- [ドキュメントとPlayground](https://github.com/manifest-configuration-language/website)
- [VS Code拡張](https://github.com/manifest-configuration-language/vscode)
- [サンプル集](https://mcl.walnuts.dev/examples/)

## 開発

miseをインストールしてから、次のコマンドで品質チェックを実行します。

```sh
mise run quality:check
```

ソースからビルドする場合は次のコマンドを使います。開発ビルドのバージョンは`main`です。

```sh
go build -o ./mcl ./cmd/mcl
./mcl version
```

## リリース

GitHub Actionsの **Release** ワークフローを `main` ブランチから実行します。

- `bump`: `patch` / `minor` / `major`（バージョンの上げ幅）
- `channel`: `stable` / `rc`（公開チャネル）

失敗時の対応は状況によって異なります。

- **CI 成功待機やビルドの段階で失敗した場合**：同じ実行の「Re-run failed jobs」で再実行してください。Release が作成される前なので、再実行で正常に公開できます。
- **npm 公開だけが失敗した場合**：公開済み Release のある同じ実行で「Re-run failed jobs」を実行してください。アセットは既にあるため、npm パッケージの公開だけをやり直します。
- **workflow ファイルの変更によって Release 作成が拒否された場合**：再実行では解決しません。最新 `main` の CI 成功を確認したうえで、新しい Release run を開始してください。
