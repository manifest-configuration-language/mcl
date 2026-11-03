# MCL

MCLは、YAMLに近いインデント構文でKubernetes Manifestを組み立てる構成言語です。言語リファレンス、チュートリアル、Playgroundは[MCL Webサイト](https://mcl.walnuts.dev/)を参照してください。

## インストール

miseの`github:`バックエンドで、GitHub Releaseから最新の安定版をインストールします。

```sh
mise use -g github:manifest-configuration-language/mcl

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

失敗時の対応は状況によって異なります。

- **CI 成功待機やビルドの段階で失敗した場合**：同じ実行の「Re-run failed jobs」で再実行してください。Release が作成される前なので、再実行で正常に公開できます。
- **npm 公開だけが失敗した場合**：公開済み Release のある同じ実行で「Re-run failed jobs」を実行してください。アセットは既にあるため、npm パッケージの公開だけをやり直します。
- **workflow ファイルの変更によって Release 作成が拒否された場合**：再実行では解決しません。最新 `main` の CI 成功を確認したうえで、新しい Release run を開始してください。
