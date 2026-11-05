# Kubernetes Schema検証時の小数変換

## 問題

ArgoCD Applicationの`spec.source.helm.valuesObject`に含まれるHistogram bucketの`0.01`が、Kubernetes Schema検証へ変換する際に拒否されていた。
`runtimeValueAsKubernetesJSON`は任意精度Decimalからfloat64への変換が二進数として完全に一致することを要求しており、JSONとKubernetesで通常使用できる`0.1`や`0.01`も拒否してしまう。

## 変更

Kubernetes用の値変換では、float64をJSONの十進表現へ戻したときに元のDecimalと等しいことを要求する。
既存の`shopspring/decimal`を使用し、二進数の近似が必要な通常の十進小数を受理する。
無限大、アンダーフロー、`0.100000000000000001`のように十進表現が変わってしまう精度の値は引き続き拒否する。
MCLのDecimal演算、通常のJSON出力、整数の範囲制限は変更しない。

## 確認対象

純粋な数値変換の回帰テストで、Histogram bucket、通常の小数、末尾ゼロ、指数表記、精度欠落、オーバーフロー、アンダーフローを確認する。
infraでは`actions-runners`のApplicationに含まれるHistogram bucketが、Manifestを補正せずに評価できることを確認する。
