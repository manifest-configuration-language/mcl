# 2026年11月6日のMCL・infra再レビュー対応

## 対象

MCLの`flatMap`と遅延参照の組み合わせ、Argo CD共通ヘルパーのMapとNodeの使い分け、名前付き引数、Cluster API設定の再利用を確認します。レビューでは新しい重大な問題は報告されていません。MCLの関数合成と既存の参照機能が期待どおり連携するかを検証し、infraのMCL記述を簡潔にします。

## `flatMap`と遅延List

`lists.flatMap()`は変換関数が遅延Listを返すと即時にエラーにしていました。`ref()`を使う条件式の結果は参照解決まで遅延するため、変換関数の戻り値が最終的にListでも処理できませんでした。

遅延したList操作を保持する継続を追加し、参照解決後に各変換結果を検証して1段平坦化します。`filter`と`exactlyOne`も同じ継続型を使い、flatMapだけの評価経路を増やさずに既存の遅延解決へ統合します。`TestEvaluateListsFlatMapResolvesDeferredListResults`で`ref()`に依存する条件分岐の結果が元の順序で展開されることを確認し、遅延した非List結果はList型エラーになることも確認します。

## Argo CD共通ヘルパーのNode同一性

`syncPolicy()`、metadata、source、generator、ApplicationSet template metadataは、通常のMapを返す共通ヘルパーです。これらから`node:`を除き、独立したApplicationやApplicationSetのルートだけにNode同一性を付与します。List記法はMCL Formatterに揃えます。

## 名前付き引数

Argo CDのApplication生成で6個の位置引数を渡していたため、`name`、`project`、`destination`、`source`または`sources`、`labels`、`syncOptions`を名前付き引数にします。引数の意味を呼び出し位置から読み取れるようにします。

## Cluster API設定の再利用

`kurumi`と`biscuit`の`resources()`が同じ`config(cluster)`を6回計算していました。関数ブロック内で一度だけ`settings`を生成し、6つのリソース生成関数へ渡します。現在のMCL構文ではローカル変数を使う関数はブロック形式で記述し、最終値をListとして返します。

## Helmの乱数関数

Helm templateで乱数関数が呼び出された場合は、同じ入力で再renderしてManifest出力を比較する既存の`RenderVerified()`を維持します。乱数関数の結果を使わず出力が同じ場合は許可し、出力が変化する場合は明示的な非決定的出力許可がない限りエラーにします。

## infraでの`flatMap`

SeaweedFSのS3 Client生成は、現在も`lists.flatMap()`でClientごとのResource Listをまとめます。infraには`ref()`に依存してList自体を切り替える既存の設定がないため、遅延Listの挙動だけを見せる目的でManifest生成へ不要な参照依存を追加しません。MCL 0.4.5をinfraへ反映し、現在の`flatMap`利用を含む全配置のrenderで実行互換性を確認します。

## Releaseとinfraへの反映

MCL 0.4.5の[release workflow](https://github.com/manifest-configuration-language/mcl/actions/runs/42369553696)は成功し、[公式release archive](https://github.com/manifest-configuration-language/mcl/releases/tag/v0.4.5)のmacOS arm64 SHA256は`SHA256SUMS`と一致しました。infraの`mise.toml`と`k8s/mcl.lock`を0.4.5とrevision `fafaf663998a8ff22b3639cb31510037097307d0`へ更新しました。

## 検証

MCLの`mise run quality:check`はGo test、golangci-lint、gofmt、WASM checkを含めて成功しました。infraの変更対象4ファイルはMCL 0.4.5の`mcl lint`と`mcl fmt --check`を通過しました。全135アプリ配置と、Cluster APIの`kurumi`・`biscuit`、Argo CD Applicationの`kurumi`・`biscuit`をrenderし、すべて成功しました。render中に`k8s/mcl.lock`の依存エントリーは変更されませんでした。
