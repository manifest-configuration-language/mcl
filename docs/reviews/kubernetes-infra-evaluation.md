# Kubernetesインフラ評価で見つかったMCLの課題

## 対象

この文書は、infraリポジトリの`k8s/`をMCLへ移行する評価で再現または確認したMCL本体の課題と改善候補を記録します。クラスタ配置方針、`app.json5`のcluster metadataに保存するprofile selector、既存Git file generatorとConfig Management Pluginの連携、移行済みアプリケーションと残作業はinfraリポジトリの`docs/design/mcl-kubernetes-evaluation.md`で管理します。

各項目では、評価で確認した挙動と未解決の改善候補を記載します。MCLの実装状況が変わり課題が解消した場合は、この文書から該当項目を削除します。

## 実装済みの改善

### Helm chartのCRDとhook lifecycle

`std:helm.render`はchart内のCRDを`bootstrap` lifecycleで出力し、対応するHelm hook annotationをMCLのlifecycle metadataへ変換します。NVIDIA GPU Operator v26.7.0では8 CRD、19通常resource、9 hook resourceを別phaseでrenderし、resource内容は`helm template --include-crds`出力と一致しました。hook annotationはlifecycle metadataに移るため、比較時はannotationではなくMCL operation phaseを使います。

### 明示的なHelm lookup snapshot

Helm templateの`lookup`は、クラスタ状態を暗黙に読み込むと同じrevisionから異なるManifestを生成します。`std:helm.render`に`lookupResources`を追加し、呼び出し側が固定したKubernetes resource snapshotを渡せるようにしました。省略時は従来どおり失敗し、明示した空Listは空のクラスタ状態として扱います。Longhorn chart v1.12.1はコメント内のtemplate条件でも`lookup`を評価するため、infra側では空snapshotを明示して再現可能なrenderを行います。

### Helmのbundled dependency version処理

Helmは親chartのdependency version constraintと一致しない同梱subchartを`ProcessDependencies`で除外してrenderします。Scylla Manager chart v1.21.1は親chartで`scylla` version `1.0.0`を要求し、同梱subchartは`0.0.0`を宣言しています。MCLの事前検証は同梱subchartの存在だけを確認し、version constraintの適用はHelmに委ねるよう変更しました。同じchart archiveからHelmとMCLが7 resourceをrenderすることを確認しました。

### remote Helm Chartのlocked dependency

infraの`iwashi-cortex`が使うCortex chart v3.3.8は、Chart.yamlとChart.lockでmemcachedを5つのaliasとして宣言しますが、配布archiveにはaliasのないmemcached chartが1つしか含まれません。MCLはrequirementsごとにsubchartの存在を検証するため、`no matching subchart is bundled`で`mcl update`とcheckが失敗していました。MCLはmissing dependencyがあるremote chartのChart.lockを検証し、HTTPS repository indexまたはOCI referenceから固定versionのdependency archiveを取得して、MCL lock/cacheへ各archiveのSHA-256を記録します。HTTPS repository indexは最大64MiBまで読み込みます。複数aliasは固定された同じpackage archiveから個別のchart instanceとして再構築します。通常のrenderとcheckはChart.lockの再解決やnetwork accessを行わず、mcl.lockとcacheのみを使います。Chart.lockとChart.yamlのdependency digestが一致しない場合や、固定versionと一致しないarchiveは拒否します。

Chart.lockを持たないchartとstaleなChart.lockを持つchartは、requirementsに対応するsubchartを親archive内に全て同梱する場合に限り、親archiveのSHA-256で固定して読み込みます。完全同梱chartではChart.lockを使わず、requirementsを満たすarchive内のsubchartを利用します。`repository: ""`と`file://` dependencyは親archiveに同梱されたsubchartを使い、同梱がない場合は失敗します。missing remote dependencyはChart.lockのrepository URLから解決します。Helmと同じくsubchartのversion constraintはrender時に適用します。外部packageを取得するときはChart.lockのdigestと固定versionを検証します。`@repo`または`alias:repo`のrepository aliasはChart.lockに記録されたrepository URLへ解決し、`apiVersion: v1` chartはHelm互換の旧dependency hashも受け入れます。

### 決定的な非暗号学的hash

infraではExternalSecretが参照するremote値を公開せず、ローカルのdata mappingやtemplateが変わったときにSecret名を変えてPod更新を促す必要があります。この用途にSHA-256は不要な暗号学的強度を持ち、既存の6桁suffixは衝突しやすいため、MCLに`std:hash.fnv1a64(value)`を追加しました。この関数は決定的なJSON bytesへ変換してFNV-1a 64-bitの全16桁を返し、秘密保護や攻撃者入力の耐衝突性には使いません。immutable resourceのcontent-addressingなど暗号学的hashを必要とする用途では`std:crypto.sha256`を引き続き使います。infra側のExternalSecret suffixの適用条件はinfraリポジトリの設計文書に記録します。

### module間での空collection型の保持

明示的な型注釈を持つ空Listまたは空Mapを別moduleからimportすると、runtime valueから型を再推論する際に型注釈が失われ、record内で参照したときtyped evaluatorが拒否する問題を確認しました。runtime valueのtype provenanceを使って空collectionの型を復元し、MCL側のregression testとinfraの`iwashi-blackbox`で確認しました。

### 宣言型からの空collection推論

型注釈を持つlet宣言とfunction return typeから空Mapと空Listのexpected typeを式へ伝播し、`Node<T>`のnested recordとconditional expression内でもMap valueとList itemを検証するようにしました。Scylla NodeConfigの`nodeSelector: {}`と`tolerations: []`は、外部の型定義からexpected typeを受け取り、collectionごとのローカル型注釈なしで検証できます。非互換のnon-empty valueは型エラーになります。

### Helm出力の複数行文字列

Helmがrender済みtemplateをmanifestへまとめる際に末尾改行を加える挙動に合わせ、MCLもYAML解析前に末尾改行を補うようにしました。Velero chart v12.1.0のConfigMap `velero`で、`data.global`の末尾改行を含むHelm出力と一致することをregression testで確認しました。

### moduleからimportした型alias

module alias経由でimportした型を別moduleの型定義で参照できるようにしました。型aliasを使ったunionのscope情報も型解析とhigher-order callbackの型比較まで維持します。KurumiとBiscuitのTart host設定でPower unionを共有し、重複定義を削除しました。MCLの全Go testとinfraのcluster設定checkで確認しました。
