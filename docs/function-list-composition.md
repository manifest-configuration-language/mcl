# 関数とList

関数は常に`()`で呼び出します。複数の設定を受け取る関数には、名前付き引数を使うと値の意味を明示できます。

```mcl
fn container(name: String, image: String, args: List<String> = []) -> Map<String, Any> =
  name: name
  image: image
  args: args

let api = container(name = "api", image = "example/api:1")
```

名前付き引数の順番は自由です。位置引数も使えますが、名前付き引数より前に置きます。引数の重複、未知の名前、必須引数の省略はエラーになります。デフォルト値は引数を省略した場合に使われ、前に宣言した引数を参照できます。デフォルト値のある引数の後に必須引数を宣言することもできます。

大きな設定は型付きレコードとして分離します。Mapのフィールドには`:`、名前付き引数には`=`を使います。

```mcl
type ServiceConfig =
  name: String
  namespace: String
  port: Int

fn service(config: ServiceConfig) -> Map<String, Any> = node:
  apiVersion: "v1"
  kind: "Service"
  metadata:
    name: config.name
    namespace: config.namespace
  spec:
    selector:
      app: config.name
    ports:
      - name: "http"
        port: config.port
        targetPort: config.port

let config: ServiceConfig =
  name: "api"
  namespace: "apps"
  port: 8080

emit service(config)
```

トップレベル関数は、ファイル内で後に宣言した関数を参照できます。通常の`let`は宣言順に評価します。

Listの中の`...items`は、`items`の要素を順番どおり展開します。展開元にはListを渡します。

```mcl
let first = [1, 2]
let combined = [0, ...first, ...[], 3]
# combinedは[0, 1, 2, 3]
```

展開は1段だけです。`...[[1], [2]]`は2つのList要素を追加します。`for`の本体がListを返す場合も結果はListのListです。複数Resourceを出力するときは、合成したListを`emit each`に渡します。

```mcl
fn resources(config: ServiceConfig) -> List<Map<String, Any>> =
  [service(config)]

let configs: List<ServiceConfig> = [config]
let groups =
  for item in configs:
    resources(item)
let all = [
  ...groups[0],
]
emit each all
```
