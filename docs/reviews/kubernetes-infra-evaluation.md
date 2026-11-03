# Kubernetesインフラ評価で見つかったMCLの課題

## 対象

この文書は、infraリポジトリの`k8s/`をMCLへ移行する評価で再現または確認したMCL本体の課題と改善候補を記録します。クラスタ配置方針、アプリケーションのplacement record、ApplicationSet Plugin Generatorの設計、移行済みアプリケーションと残作業はinfraリポジトリの`docs/design/mcl-kubernetes-evaluation.md`で管理します。

各項目では、評価で確認した挙動と未解決の改善候補を記載します。MCLの実装状況が変わり課題が解消した場合は、この文書から該当項目を削除します。

## 実装済みの改善

### Helm chartのCRDとhook lifecycle

`std:helm.render`はchart内のCRDを`bootstrap` lifecycleで出力し、対応するHelm hook annotationをMCLのlifecycle metadataへ変換します。NVIDIA GPU Operator v26.7.0では8 CRD、19通常resource、9 hook resourceを別phaseでrenderし、resource内容は`helm template --include-crds`出力と一致しました。hook annotationはlifecycle metadataに移るため、比較時はannotationではなくMCL operation phaseを使います。

### 決定的な非暗号学的hash

infraではExternalSecretが参照するremote値を公開せず、ローカルのdata mappingやtemplateが変わったときにSecret名を変えてPod更新を促す必要があります。この用途にSHA-256は不要な暗号学的強度を持ち、既存の6桁suffixは衝突しやすいため、MCLに`std:hash.fnv1a64(value)`を追加しました。この関数は決定的なJSON bytesへ変換してFNV-1a 64-bitの全16桁を返し、秘密保護や攻撃者入力の耐衝突性には使いません。immutable resourceのcontent-addressingなど暗号学的hashを必要とする用途では`std:crypto.sha256`を引き続き使います。infra側のExternalSecret suffixの適用条件はinfraリポジトリの設計文書に記録します。

## 未解決の機能不足

### module間の型参照

`import "./types.mcl" as types`で読み込んだmoduleがexportする型を、別moduleの型定義から`types.Power`のように参照すると、現行MCLはunknown typeとして拒否します。複数clusterのTart Power Unionを共有できず、同じ型定義を複数moduleに重複しています。qualified type importを実装するか、共有型を展開できる機能が必要です。

### 空のMapとListの型推論

MCLは空のMapやListの要素型を推論できない場合があります。Scylla NodeConfigの空の`nodeSelector`や`tolerations`では、それぞれの値にローカルな型注釈が必要でした。空collectionを含むrecordの型を宣言から推論するか、明示的な型注釈を簡潔に書ける構文が改善候補です。

### Helm dependency metadataの不一致

Scylla Manager chart v1.21.1の親chartは`scylla` dependencyのversion `1.0.0`を宣言しますが、同じarchiveに含まれる`scylla` subchartの`Chart.yaml`はversion `0.0.0`を記録しています。Helmはこのarchiveから7 resourceをrenderしますが、MCL 0.3.20の`mcl update`はdependency versionを照合し、matching subchartがないとして失敗します。確認したparent archiveのSHA-256は`2a2bdbda299dcb6314181fc285ec179a99d9b7009f23269be23cb24962981715`です。MCLがpackage内のsubchartをどのようにdependency constraintへ対応付けるかをHelmの挙動と照合し、親chart archiveのhash固定を保ったまま不一致を扱う選択肢を評価します。

### Helm chart内の証明書生成

rabbitmq-topology-operator 0.4.1は`useCertManager: true`を指定しても、`templates/validating-webhook-configuration.yaml`で`genCA`を呼び出します。MCLのhermetic Helm renderingは`genCA`を無効にしているため、`mcl update`が失敗します。random関数を無条件に許可せずにこのchartをrenderできるよう、chartが既存の証明書を受け取る設定、または決定的な証明書入力を扱う方法を確認する必要があります。

### 再現性と証明書生成

trust-manager 0.24.0はHelm template内で`genCA`を使ってランダムな証明書を生成します。MCLは再現可能な評価を保つため、このようなrandom関数を無効にしています。決定性を損なわずに生成結果を固定する入力方法が必要か、chart側の値で証明書生成を無効にできるかを個別に評価します。random関数を無条件に許可する変更は改善案に含めません。
