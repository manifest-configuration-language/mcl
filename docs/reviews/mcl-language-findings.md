# MCL言語設計上の指摘

## Kubernetes Schemaからの構築時型推論

`node:`内で`apiVersion`と`kind`が静的に決まる場合でも、NodeExpr本文は既知のKubernetes Schemaを期待型として受け取らず、`Any`を含むMap値が構築中に誤りとして検出されないことがあります。たとえばinfraの`AIServiceSpec.resources`は`Map<String, Any>`として公開され、DeploymentのContainer ResourceRequirementsへ渡されます。現在の出力検証は誤った値を検出しますが、型エラーはモジュール評価の後に出ます。

対象となるコード境界は`internal/engine/static_expr_types.go`のNodeExpr推論と、`internal/engine/typed_ir_typecheck.go`のNodeExpr本文検査です。最初の実装では、静的GVKに対してのみ`k8sschema.Builtin`からExpectedTypeを取得し、既存のMap/List型検査へ渡します。既知の閉じたSchemaの未知フィールドも、Map型検査で明示的に拒否する必要があります。Required判定と動的な`Any`値はこの変更に混ぜず、CRDはCatalogの読込順序を決めてから別途扱います。

infraの`node:`式は415行あり、組み込みGVKも少なくとも202件ありました。現在の全配置評価で構築時に修正が必要な不正Resourceは確認していません。この指摘はManifest出力の正しさではなく、エラー位置を早めることと、Map<String, Any>を介した入力検査の精度を上げる提案です。設計を確定する場合は、静的GVK、nested field、List item、optional field、unknown field、dynamic Any値を対象にMCLテストを追加し、infra全配置の評価を行います。

## CRD Catalogの評価ごとの再コンパイル

成功した104配置の評価時間は平均67.6秒、中央値65.1秒でした。アプリごとにCLIを別プロセスとして起動するため、プロセス内コンテンツキャッシュでは同一処理を避けられません。

この評価負荷を減らすため、組み込みGVKだけを出力する評価では外部Catalog sourceを読まず、Catalog登録時のRuntimeValue変換とValidator構築を遅延させます。GVK、scope、served、source location、元YAML nodeを保持し、実際に使うSchemaだけをRuntimeValueへ変換してコンパイルします。詳細、検証範囲、性能比較は[CRD Catalogの遅延読み込みとSchemaコンパイル](crd-catalog-lazy-compilation.md)に記録します。
