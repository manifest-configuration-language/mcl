# CRD Catalogの遅延読み込みとSchemaコンパイル

## 問題

各アプリを個別のMCLプロセスで評価すると、`crd_schemas`に登録された全CustomResourceDefinitionを毎回読み込み、served versionごとのRuntimeValue、Structural Schema、OpenAPI Validator、CEL Validatorを構築していました。成功した104配置の評価時間は平均67.6秒、中央値65.1秒でした。

CatalogのSchemaを使わないアプリも外部Schemaの読み込み時間を負担していました。プロセス内キャッシュはアプリごとに起動するCLIで再利用されません。

## 設計

MCLは各評価の開始時に`mcl.toml`を読み、`crd_schemas`の宣言とsource optionを検証します。Manifestを評価した後、次の条件のどれかを満たす場合だけ、宣言された全てのCRD Schema sourceを解決します。

- Artifactが`CustomResourceDefinition`を出力する。
- ArtifactのGVKに対応する組み込みKubernetes Resource descriptorが存在しない。
- ArtifactのGVKに対応する組み込みKubernetes Schemaが存在しない。
- `mcl update`を実行する。

組み込みResourceは、組み込みdescriptorと`apiVersion`・`kind`に対応する組み込みSchemaの両方で判定します。カタログを読み込んだ場合は、Catalogに同じGVKが存在するかに関係なく、従来どおり出力Artifactを検査します。`mcl update`はResource種別に関係なく全sourceを読み、lockを更新します。

Catalog sourceのYAMLは`yaml.Node`として読み、入力サイズ、階層、ノード数、タグ、Map key、重複key、Scalar形式を検査します。全schemaのRuntimeValueを作らず、CRDのGVK、scope、served、source location、`openAPIV3Schema`のYAML nodeだけを保持します。

Catalog Schemaのcanonical fingerprintは重複GVKを比較するときだけ作ります。OpenAPI Validator、Structural Schema、CEL Validator、MCL型情報は、出力Resourceに一致するGVKがあるときだけ作ります。Schema nodeはその時点でRuntimeValueへ変換し、既存のdeterministic JSON経路でコンパイルします。

出力ArtifactがCRDの場合、CatalogとのGVK衝突を検査するため、通常評価でも設定済みsourceを読み込みます。出力CRD自体は従来どおり`ValidateCustomResourceDefinition`でCRD全体と全served versionを検証します。

## 検証範囲

全Catalog sourceのlock、YAML構文、入力上限、YAML node形式、CRD文書の存在、GVK、scope、served version、重複version、同一GVKのSchema競合は、Catalog読み込み時に検査します。

Catalog CRD文書は、Kubernetesの`ValidateCustomResourceDefinition`によるCRD全体の検証を受けません。Catalog登録はGVK、scope、served、`openAPIV3Schema`の索引作成に必要な項目だけを検証します。storage version数など索引作成に関係しないCRD制約は、Catalog sourceに対して検査されません。

Manifestに使われないSchemaのStructural Schema、OpenAPI制約、CEL式はコンパイルしないため、その評価では妥当性を検査しません。該当GVKのResourceを出力した評価では、そのSchemaをコンパイルしてunknown field、defaulting、OpenAPI、list-map/set、CELを検証します。コンパイルエラーは元のSchema source locationを示します。

出力Resourceが全て組み込みGVKであり、CRD Artifactがない評価は外部Catalog sourceを読みません。`mcl.toml`の設定宣言は検証するため、source指定ミスや不正optionは引き続き評価時に検出します。

## 性能確認

Catalog遅延処理前の104配置は平均67.6秒、中央値65.1秒でした。同一infra snapshotの開発中CLIで再評価したところ、`local-path-provisioner`は64.42秒から0.41秒、カスタムResourceを出力する`keda-http-addon`は65.85秒から1.76秒になりました。

別のカスタムResourceを含む6配置も全て成功し、並列4 workerで0.56秒から10.42秒でした。配置ごとの時間にはHelm chartの展開量と並列実行によるCPU競合が含まれます。公開版CLIでの全配置評価と、同じ環境で直列に実行した比較をRelease後に最終結果として記録します。

## 状態

GVK判定によるCatalog sourceの遅延読み込みと`yaml.Node`を使った軽量なCatalog登録を実装しています。MCLの最終テスト、Release、公開版CLIを使ったinfra全配置の評価後に、公開バージョンと結果を記録します。
