# ArgoCDからの型付きパラメータ入力（実装済み）

## 問題

ArgoCD CMPからMCLへクラスタ情報、アプリのFeature、Settingsを渡すとき、値をコマンド文字列へ埋め込むとシェルの引用規則に依存します。文字列、配列、入れ子のオブジェクトを安全に渡すには、CMPがJSONファイルを生成し、MCLがファイルを直接読む入力経路が必要です。

## 実装

`mcl render`、`check`、`explain`、`diff`、`update`は、繰り返し指定できる`--parameter-file path.json`から実行時パラメータを読み込みます。各ファイルは重複キーを含まないトップレベルJSONオブジェクトである必要があり、オブジェクトのキーと値をパラメータ名と値として扱います。既存の`--parameter name=<JSON value>`も併用できます。同じパラメータ名を`--parameter`で複数回指定した場合、または複数ファイルと`--parameter`の間で指定した場合は、重複した名前と入力元を示して失敗します。

CMPは値をシェルコマンドへ補間せず、パラメータをJSONファイルへ書いてMCLへファイルパスを渡せます。JSON値はMCLの既存の型検査を通るため、Cluster factsとアプリ設定を型付きで検査できます。デプロイ先の列挙やApplicationSetの検証はこのCLI入力機能の責務に含めません。

## 使用例

```sh
mcl render apps/cilium/main.mcl \
  --parameter-file /work/cluster.json \
  --parameter-file /work/cilium-settings.json
```

各ファイルは、例えば`{"cluster": {"kubernetesVersion": "1.35"}}`のようにJSONオブジェクトを格納します。

## MCL関数を使う場合の注意点

ArgoCD Applicationの共通生成関数は、`Map<String, Any>`を返す関数へ値を渡す形で表現できます。MCL v0.3.29は、`helm.render`の第2引数に`options(cluster)`のような`Map<String, Any>`を返す関数呼び出しを渡すと、型検査時にMapとして扱わず失敗します。MCLソースではMap型を認識し、`helm.render`の戻り値を`List<Map<String, Any>>`として扱う修正を加えました。v0.3.30から次の形を使えます。

```mcl
fn options(cluster: Cluster) -> Map<String, Any> =
  kubeVersion: cluster.kubernetesVersion

fn chartResources(cluster: Cluster) -> List<Map<String, Any>> =
  helm.render(chartArchive, options(cluster))
```

関数や値の参照先は、MCL v0.3.29では呼び出し元より前に宣言してください。例えば`resource(cluster)`から`sources(cluster)`と`syncOptions`を参照する場合は、`sources`関数と`syncOptions`を`resource`関数より前へ置くと型検査できます。

`Map<String, Any>`の値を参照すると、そのフィールドの静的型も`Any`になります。MCL v0.3.29では、その値を`lists.replaceAt`へ渡すとList型を確認できず、Mapを更新する関数全体を型付き評価器が拒否します。MCLソースでは、`Any`のままフィールド参照とList置換を型付きIRへ変換し、実行時に値がListか確認する対応を加えました。既知のString値をList引数へ渡した場合は、引き続き静的型検査で拒否します。v0.3.30から、`Map<String, Any>`を返すHelmやMCLヘルパーの結果を同じ関数で変換できます。

## Cilium設定の修正

MCL v0.3.29でCiliumの`values.mcl`を検査すると、`body return type cannot be inferred`というエラーが出ました。原因は、`Settings`が`envoyMemory`を宣言しているのに、`helmValuesWithFlags`が`settings.envoy_memory`を参照していたことです。Infra側は参照を`settings.envoyMemory`に修正しました。このエラーは原因となったフィールド名を示さないため、同じエラーが出た場合は、関数本体の型名、フィールド名、参照順序を確認してください。

## 組み込みKubernetes Schemaの任意項目とnull

CiliumのDeploymentの`priorityClassName`、OpenCostのContainerの`args`とPodの`initContainers`、Argo CDのRoleの`rules`には、Chartが未指定値を`null`として出力する例があります。これらは組み込みSchemaで必須とされていない項目ですが、従来の`normalizeToNullable`は子Schemaを持たないobject/arrayだけをnull許容にし、正当なChart出力を拒否していました。

組み込みValidatorの正規化を、親Schemaの`required`集合にないpropertyだけを`anyOf: [元Schema, null]`へ変換する実装に変更しました。必須property、配列要素、mapの各値を一律にnull許容にはしません。CRDのStructural SchemaとCEL検証は変更しません。アプリごとの出力補正関数は不要になります。

回帰テストでは、Podの任意項目とRoleの`rules: null`を受け付け、必須の`containers: null`、コンテナ配列のnull要素、ConfigMapのnullデータ値を拒否することを確認します。ソース構築時の型推論と、出力後の組み込みSchema検証は別の検査であり、この変更は後者を対象にしています。

## CRD Webhook Service Portの既定値

Cluster APIのcore v1.14.2が生成するCRDでは、conversion webhookのService portが省略されています。Kubernetes API Serverは省略時に443を設定しますが、MCLはCRDを内部型へ変換する際にAPI型のdefaultingを実行せず、port 0としてCRDを検証していました。

CRDの内部型への変換前に、apiextensions/v1が提供する`SetObjectDefaults_CustomResourceDefinition`を呼び出すよう変更しました。省略されたService portはKubernetesと同じ443になり、明示された0は既定値で上書きせず検証エラーになります。CRD Schemaの実行時defaultingとCEL検証には変更を加えません。
