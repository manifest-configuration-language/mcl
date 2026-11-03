# MCL

MCLは、YAMLに近いインデント構文でKubernetes Manifestを組み立てる構成言語です。言語リファレンス、チュートリアル、Playgroundは[MCL Webサイト](https://mcl.walnuts.dev/)を参照してください。

## インストール

miseの`github:`バックエンドで、GitHub Releaseから最新の安定版をインストールします。

```sh
mise use -g github:manifest-configuration-language/mcl@latest

mcl version
```

miseはGitHub Releaseの添付ファイルからOSとアーキテクチャに合うアーカイブを選択して展開します。`latest`はprereleaseを除外します。

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

ソースからビルドする場合は次のコマンドを使います。開発ビルドのバージョンは`main`です。

```sh
go build -o ./mcl ./cmd/mcl
./mcl version
```

## リリース

GitHub Actionsの **Release** ワークフローを `main` ブランチから実行します。

- `bump`: `patch` / `minor` / `major`（バージョンの上げ幅）
- `channel`: `stable` / `rc`（公開チャネル）

途中で失敗した場合は、新しい実行を開始せず、失敗した実行の「Re-run failed jobs」で再実行してください。
