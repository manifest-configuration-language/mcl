# 2026年11月6日のMCL・infra再レビュー対応

## 対象

MCLの`flatMap`と遅延参照の組み合わせ、Argo CD共通ヘルパーのMapとNodeの使い分け、名前付き引数、Cluster API設定の再利用を確認します。レビューでは新しい重大な問題は報告されていません。MCLの関数合成と既存の参照機能が期待どおり連携するかを検証し、infraのMCL記述を簡潔にします。

## `flatMap`と遅延List

`lists.flatMap()`は変換関数が遅延Listを返すと即時にエラーにしていました。`ref()`を使う条件式の結果は参照解決まで遅延するため、変換関数の戻り値が最終的にListでも処理できませんでした。

遅延したList操作を保持する継続を追加し、参照解決後に各変換結果を検証して1段平坦化します。`filter`と`exactlyOne`も同じ継続型を使い、flatMapだけの評価経路を増やさずに既存の遅延解決へ統合します。`TestEvaluateListsFlatMapResolvesDeferredListResults`で`ref()`に依存する条件分岐の結果が元の順序で展開されることを確認します。

## Argo CD共通ヘルパーのNode同一性

`syncPolicy()`、metadata、source、generator、ApplicationSet template metadataは、通常のMapを返す共通ヘルパーです。これらから`node:`を除き、独立したApplicationやApplicationSetのルートだけにNode同一性を付与します。List記法はMCL Formatterに揃えます。

## 名前付き引数

Argo CDのApplication生成で6個の位置引数を渡していたため、`name`、`project`、`destination`、`source`または`sources`、`labels`、`syncOptions`を名前付き引数にします。引数の意味を呼び出し位置から読み取れるようにします。

## Cluster API設定の再利用

`kurumi`と`biscuit`の`resources()`が同じ`config(cluster)`を6回計算していました。関数ブロック内で一度だけ`settings`を生成し、6つのリソース生成関数へ渡します。現在のMCL構文ではローカル変数を使う関数はブロック形式で記述し、最終値をListとして返します。

## 検証

`TestEvaluateListsFlatMapResolvesDeferredListResults`は、Nodeのフィールドを参照する条件分岐から返したListが、参照解決後に順序を保って平坦化されることを確認します。MCLの`mise run quality:check`はGo test、golangci-lint、gofmt、WASM checkを含めて成功しました。

infraの変更対象4ファイルはMCL 0.4.4の`mcl lint`と`mcl fmt --check`を通過しました。MCL 0.4.5のrelease後にtoolchain lockを更新し、全配置のrender結果を追記します。
