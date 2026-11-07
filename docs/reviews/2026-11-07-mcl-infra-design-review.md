# 2026年11月7日 MCL・infra設計レビュー対応

## 設計方針

今回のレビューでは、infraにあるManifestの動作回帰を修正し、MCLには複数の評価処理で再利用できる機能だけを追加します。Kubernetes Schemaによる検証はMCLの汎用的なMapやNodeをKubernetes専用構文へ変更せず、静的に分かる情報だけを利用します。

## Lockfileに固定した依存取得

Argo CD CMPは、Manifestのrender前に`mcl update`を実行してLockfileが変化しないか確認していました。`update`は依存元へ接続して更新候補を解決する操作なので、確定済みの依存を取得する目的には使いません。

`mcl fetch --locked <file>`は既存の`mcl.lock`に登録された依存だけを取得します。Lockfileは変更せず、キャッシュ内のデータをSHA-256で検証し、未登録依存とダイジェスト不一致をエラーにします。正常なキャッシュがない依存は、Lockfileに記録されたGit revision、Helm version、またはコンテンツダイジェストを指定して取得します。`mcl render`は依存を更新しないため、キャッシュにない依存が必要な場合は先に`mcl fetch --locked`を実行します。

Argo CD CMPは`mcl.lock`のMCL toolchainと同じバイナリを選び、`mcl fetch --locked`と`mcl render`へ同一のparameter fileを渡します。選択したクラスタの`kubernetesVersion`も両コマンドへ渡します。配置のnamespace overrideはApplicationSetで解決し、`destination.namespace`と`MCL_NAMESPACE`に同じ値を設定します。deployment parameterには予約済みの`namespace`を含めません。

## Kubernetesバージョン別Schema

MCLは組み込みKubernetes OpenAPI Schemaとして`v1.36.4`と`v1.37.1`を持ちます。`--kubernetes-version`を省略したときは既定値の`v1.37.1`を使用します。`render`、`check`、`explain`、`diff`、`update`、`fetch`と評価APIの`EvaluationOptions.KubernetesVersion`で対象バージョンを選択できます。未収録バージョンは暗黙に最新Schemaへ置き換えず、対応バージョンを列挙したエラーにします。

各Schema artifactは、Kubernetes公式リポジトリの不変commitから取得した`api/openapi-spec/swagger.json`をgzip圧縮して格納します。`schema_source.go`にはKubernetes version、source commit、展開後JSONのSHA-256、embed artifact、バージョン別Catalog loaderを対応づけます。Catalog構築時に展開後のJSONをハッシュ検証してから解析し、バージョンごとに一度だけ初期化します。

新しいKubernetesバージョンを追加する場合は、対象releaseに対応する不変commitから公式Swagger JSONを取得し、gzipのmtimeを固定して`internal/k8sschema/data/kubernetes-<version>.swagger.json.gz`へ格納します。SHA-256は圧縮後ではなく展開後JSONに対して計算します。`schema_source.go`へversion、commit、digest、embed変数、`sync.OnceValues` loader、正規化switch、対応バージョン一覧、version別Catalog選択を追加します。新旧Schema間で存在が異なるAPIを使ったテストを追加し、正しいCatalogが選択されることを確認します。Catalogとvalidatorのキャッシュキーには必ずKubernetes versionを含めます。

## `node:`構築時のSchema型検査

`node:`のbodyがMapで、`apiVersion`と`kind`が文字列リテラルの場合、選択済みCatalogの対応SchemaをMap・Listの子要素へ期待型として伝播します。List Spreadのリテラル要素と静的に分かるList element typeも検査します。既知のフィールド名、インラインMap/List、静的に分かるスカラーの型をNodeの定義位置で検査し、未知フィールドと型不一致には`MCL-K8S`診断を返します。

GVKまたは値の型を静的に決定できない式、`Any`、動的Map key、Catalogに登録されていないCRDは、この静的検査で拒否しません。既存の実行時Schema検証とCRD Catalogの遅延読み込みを継続します。MCLはAPI Serverと同じ完全な意味検証を静的に再現しません。

## `helm.render`オプションの静的型情報

`helm.render`の第2引数が静的に分かるMapまたはRecordの場合、実行時が受け付ける`values`、`revision`、`releaseName`、`namespace`、`operation`、`kubeVersion`、`version`、`now`、`apiVersions`、`lookupResources`を検査します。未知キーと確定できる型不一致を呼び出し元で診断し、`operation`の文字列リテラルは`create`、`update`、`delete`、`rollback`、`verify`に限定します。

Mapの値が動的な場合や`Any`の場合は静的に拒否せず、既存の`helmOptions()`で実行時検証します。`helm.render`のMapに設定しない`allowNondeterministicOutput`は、既存どおり評価オプションから制御します。Language Serverの補完はoption名と型を同じoption定義から表示します。

## infraのManifest変更

Akvoradoのimmutable ConfigMapは、4つの設定ファイルから作る`configMapData`のハッシュを名前へ含めます。ConfigMapとOrchestratorのvolume参照は同じ`configMapName`を利用します。設定本文を変更するとConfigMap名とDeployment参照が同時に変わります。

Cluster API control-planeでは、Secret名を実際にSecretへ保存するpatch本文から生成します。TartMachineTemplate名はschematic ID、Talos version、host selectorから生成し、TartBootstrapConfigTemplate名は参照するSecret名から生成します。リソースごとの名前ハッシュは、そのリソースの実効設定または参照先が変わる場合にだけ変わります。

ApplicationSetはアプリのtop-level namespaceを既定値として維持し、cluster placementごとの`namespace`を優先します。同じ解決済みnamespaceをArgo CD destinationと`MCL_NAMESPACE`へ渡し、MCL deployment parameterからplacement用の`namespace`キーを除外します。

通常のlabels Mapから参照同一性を付与する`node:`を除きます。不要な`settings`引数も関数から削除し、引数の意味が明白でないArgo CD Application helper呼び出しには名前付き引数を使用します。

## 互換性と責務

既存のManifestからの移行手順は対象外です。Kubernetes Schemaの対象versionはMCLまたはCMPを呼び出す側が明示し、MCLがアプリ固有のcluster設定を暗黙に読み取りません。`node:`はKubernetes resourceだけに制限しません。CRDや動的GVKは既存の実行時検証経路を使います。

## 検証

実装完了後、MCLの関連package testと`go test ./...`をまとめて実行します。MCLのrelease workflowが成功した後、infraのLockfileをそのreleaseへ更新し、Argo CD CMPのlocked fetchとrenderを使って全配置およびCluster API配置を確認します。Kubernetes clusterへのapplyは行いません。

## `int-or-string` Schema型

MCL 0.4.6の公開後にinfraの全配置をCMP経由でrenderしたところ、`actions-runners/kurumi`のNetworkPolicyが整数の`port`を含む正しい設定にもかかわらず、型付き評価で失敗しました。Kubernetes OpenAPIはNetworkPolicyPortの`port`を`format: int-or-string`として公開しますが、MCLの静的型モデルが`String`だけに変換していました。

`k8sschema`は`format: int-or-string`を`Int | String`として型モデルへ変換します。v1.36.4 Schemaを選択し、整数の`NetworkPolicyPort`を`Map<String, Any>`から生成する回帰テストで、静的Schema検査と型付き評価が通ることを確認します。

## 実装結果

MCL本体の品質確認、MCL 0.4.7のrelease workflow、infraの135アプリ配置とkurumi・biscuitのCluster API配置に対するCMPのlocked fetchおよびrenderを、完了後に記録します。Kubernetes clusterへのapplyは行いません。
