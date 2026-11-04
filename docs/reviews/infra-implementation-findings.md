# infra実装上の指摘

## 外部CRD Schema Catalogの設定

`k8s/mcl.toml`にCRD Schema Catalogが設定されておらず、CRD定義もプロジェクト内にありません。MCLは同じ評価結果にもプロジェクト設定にもSchemaがないGVKを拒否するため、ExternalSecretやHTTPRouteなどのCRDを使うアプリを単独で評価できません。`k8s/mcl.toml`から参照できる、バージョン管理されたCRD Schemaを登録する必要があります。
