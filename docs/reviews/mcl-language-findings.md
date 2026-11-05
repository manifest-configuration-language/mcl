# MCL言語設計上の指摘

## Kubernetes Schemaからの構築時型推論

`node:`内で`apiVersion`と`kind`が静的に決まる場合でも、MCLは既知のKubernetes Schemaを構築中の値の型検査に十分活用していません。既知のGVKではSchemaから型を推論し、`spec`の構築時に誤ったフィールド名や値の型を検出できる必要があります。CRDもプロジェクトの`crd_schemas`と同一評価内のCRD定義から型を解決する必要があります。

## CRD Catalogの評価ごとの再コンパイル

成功した104配置の評価時間は平均67.6秒、中央値65.1秒でした。アプリごとにCLIを別プロセスとして起動するため、プロセス内コンテンツキャッシュでは同一処理を避けられません。

この評価負荷を減らすため、組み込みGVKだけを出力する評価では外部Catalog sourceを読まず、Catalog登録時のRuntimeValue変換とValidator構築を遅延させます。GVK、scope、served、source location、元YAML nodeを保持し、実際に使うSchemaだけをRuntimeValueへ変換してコンパイルします。詳細、検証範囲、性能比較は[CRD Catalogの遅延読み込みとSchemaコンパイル](crd-catalog-lazy-compilation.md)に記録します。
