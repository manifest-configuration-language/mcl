# Kubernetesインフラ評価で見つかったMCLの課題

## 対象

この文書は、infraリポジトリの`k8s/`をMCLへ移行する評価で再現または確認したMCL本体の課題と改善候補を記録します。クラスタ配置方針、アプリケーションのplacement record、ApplicationSet Plugin Generatorの設計、移行済みアプリケーションと残作業はinfraリポジトリの`docs/design/mcl-kubernetes-evaluation.md`で管理します。

各項目では、評価で確認した挙動と未解決の改善候補を記載します。MCLの実装状況が変わり課題が解消した場合は、この文書から該当項目を削除します。

## 実装済みの改善

### Helm chartのCRDとhook lifecycle

`std:helm.render`はchart内のCRDを`bootstrap` lifecycleで出力し、対応するHelm hook annotationをMCLのlifecycle metadataへ変換します。NVIDIA GPU Operator v26.7.0では8 CRD、19通常resource、9 hook resourceを別phaseでrenderし、resource内容は`helm template --include-crds`出力と一致しました。hook annotationはlifecycle metadataに移るため、比較時はannotationではなくMCL operation phaseを使います。

### 明示的なHelm lookup snapshot

Helm templateの`lookup`は、クラスタ状態を暗黙に読み込むと同じrevisionから異なるManifestを生成します。`std:helm.render`に`lookupResources`を追加し、呼び出し側が固定したKubernetes resource snapshotを渡せるようにしました。省略時は従来どおり失敗し、明示した空Listは空のクラスタ状態として扱います。Longhorn chart v1.12.1はコメント内のtemplate条件でも`lookup`を評価するため、infra側では空snapshotを明示して再現可能なrenderを行います。

### Helmのbundled dependency version処理

Helmは親chartのdependency version constraintと一致しない同梱subchartを`ProcessDependencies`で除外してrenderします。Scylla Manager chart v1.21.1は親chartで`scylla` version `1.0.0`を要求し、同梱subchartは`0.0.0`を宣言しています。MCLの事前検証は同梱subchartの存在だけを確認し、version constraintの適用はHelmに委ねるよう変更しました。同じchart archiveからHelmとMCLが7 resourceをrenderすることを確認しました。

### 決定的な非暗号学的hash

infraではExternalSecretが参照するremote値を公開せず、ローカルのdata mappingやtemplateが変わったときにSecret名を変えてPod更新を促す必要があります。この用途にSHA-256は不要な暗号学的強度を持ち、既存の6桁suffixは衝突しやすいため、MCLに`std:hash.fnv1a64(value)`を追加しました。この関数は決定的なJSON bytesへ変換してFNV-1a 64-bitの全16桁を返し、秘密保護や攻撃者入力の耐衝突性には使いません。immutable resourceのcontent-addressingなど暗号学的hashを必要とする用途では`std:crypto.sha256`を引き続き使います。infra側のExternalSecret suffixの適用条件はinfraリポジトリの設計文書に記録します。

### module間での空collection型の保持

明示的な型注釈を持つ空Listまたは空Mapを別moduleからimportすると、runtime valueから型を再推論する際に型注釈が失われ、record内で参照したときtyped evaluatorが拒否する問題を確認しました。runtime valueのtype provenanceを使って空collectionの型を復元し、MCL側のregression testとinfraの`iwashi-blackbox`で確認しました。

## 未解決の機能不足

### module間の型参照

`import "./types.mcl" as types`で読み込んだmoduleがexportする型を、別moduleの型定義から`types.Power`のように参照すると、現行MCLはunknown typeとして拒否します。複数clusterのTart Power Unionを共有できず、同じ型定義を複数moduleに重複しています。qualified type importを実装するか、共有型を展開できる機能が必要です。

### 空のMapとListの型推論

MCLは型注釈のない空MapやListの要素型を推論できない場合があります。Scylla NodeConfigの空の`nodeSelector`や`tolerations`では、それぞれの値にローカルな型注釈が必要でした。空collectionを含むrecordの型を宣言から推論するか、明示的な型注釈を簡潔に書ける構文が改善候補です。

### Helm出力の複数行文字列

Velero chart v12.1.0のHelm出力では、ConfigMap `velero`の`data.global`にJSON文字列の末尾改行があります。`std:helm.render`のMCL出力では同じ文字列から末尾改行が失われ、Helm出力との比較でこの値だけが異なりました。出力上の改行を保持し、chartの通常resourceをHelm出力と構造的に同じ文字列にする必要があります。

### 再現性と証明書生成

trust-manager 0.24.0はHelm template内で`genCA`を使ってランダムな証明書を生成します。MCLは再現可能な評価を保つため、このようなrandom関数を無効にしています。決定性を損なわずに生成結果を固定する入力方法が必要か、chart側の値で証明書生成を無効にできるかを個別に評価します。random関数を無条件に許可する変更は改善案に含めません。
