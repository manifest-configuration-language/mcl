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

CLIの配布バイナリは`mise run release:cli`で生成します。
