# MCL言語設計上の指摘

## Kubernetes Schemaからの構築時型推論

`node:`内で`apiVersion`と`kind`が静的に決まる場合でも、MCLは既知のKubernetes Schemaを構築中の値の型検査に十分活用していません。既知のGVKではSchemaから型を推論し、`spec`の構築時に誤ったフィールド名や値の型を検出できる必要があります。CRDもプロジェクトの`crd_schemas`と同一評価内のCRD定義から型を解決する必要があります。

## 入れ子のListExprとNodeExprの型付き評価

構文上有効な入れ子のリスト式や`node:`式を含む評価で、MCLが外側の式全体を`typed evaluator`未対応として拒否します。infraのSeaweedFS評価は`ListExpr`、PostgreSQL評価は`NodeExpr`を未対応として失敗します。MCLは構文木の外側の式で失敗を報告せず、内側の式の型情報を保持して評価できる必要があります。

## CRD Catalogの評価ごとの再コンパイル

プロジェクト設定に`crd_schemas`を指定すると、各評価でCRDファイルを読み込み、Structural Schema、OpenAPI Validator、CEL Validatorを再構築します。複数のエントリーポイントを個別評価する利用方法では同じSchemaのコンパイルが繰り返されます。実際の性能影響は測定されていないため、Catalogを使う代表的な評価でコンパイル時間と割り当て量を計測し、必要な場合にのみコンテンツハッシュで再利用するべきです。
