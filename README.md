# MCL

MCLは、YAMLに近いインデント構文でKubernetes Manifestを組み立てる構成言語です。言語リファレンス、チュートリアル、Playgroundは[MCL Webサイト](https://mcl.walnuts.dev/)を参照してください。

## インストール

Go 1.27.2以降でCLIをビルドできます。

```sh
go install github.com/manifest-configuration-language/mcl/cmd/mcl@latest
mcl version
```

プロジェクトからビルドする場合は次のコマンドを使います。

```sh
go build -o ./mcl ./cmd/mcl
./mcl help
```

CLIは`check`、`lint`、`fmt`、`render`、`update`、`lsp`、`completion`などのコマンドを提供します。使い方は`mcl help`で確認できます。

## 関連リポジトリ

- [ドキュメントとPlayground](https://github.com/manifest-configuration-language/website)
- [VS Code拡張](https://github.com/manifest-configuration-language/vscode)
- [サンプル集](https://mcl.walnuts.dev/examples/)

## 開発

miseをインストールしてから、次のコマンドで品質チェックを実行します。

```sh
mise run quality:check
```

CLIのバイナリは`go build -o ./mcl ./cmd/mcl`で生成します。

## リリース

バージョンはGitタグから自動決定されます。`packages/browser/package.json`と`packages/language/package.json`の`0.0.0-development`は開発用の固定値で、CIとリリースの各ワークフローがビルド時に実際のバージョンを書き込みます。この値を手動で更新しないでください。

リリースはGitHub Actionsの`Release`ワークフローを`main`から実行します。`bump`で`patch`、`minor`、`major`を選び、`channel`で`stable`または`rc`を選びます。`rc`はnpmの`next`タグ、`stable`はnpmの`latest`タグに公開されます。`rc`の連番（`-rc.N`）は既存のタグから自動採番されます。

stableは、最新の安定版タグと同じコミットから再リリースできません。RCは、同じバージョン系列の直近RCと同じコミットからは再リリースできません。RCの連番は、そのバージョン系列で未リリースのコミットに対してのみ進みます。stableへの昇格は、RCと同じコミットから実行できます。

リリースが途中で失敗した場合は、新しい実行を開始しないでください。失敗した実行の「Re-run failed jobs」を使うと、元のコミット・入力・採番済みバージョンが維持され、公開済みのnpmパッケージとの整合性を確認しながら残りの処理を再開できます。
