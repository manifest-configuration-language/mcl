# MCL言語設計上の指摘

## Kubernetes Schemaからの構築時型推論

`node:`内で`apiVersion`と`kind`が静的に決まる場合でも、MCLは既知のKubernetes Schemaを構築中の値の型検査に十分活用していません。既知のGVKではSchemaから型を推論し、`spec`の構築時に誤ったフィールド名や値の型を検出できる必要があります。CRDもプロジェクトの`crd_schemas`と同一評価内のCRD定義から型を解決する必要があります。
