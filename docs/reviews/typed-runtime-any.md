# `Any`から構造を読む式の型付け

## 問題

設定Mapの値は`Any`として宣言されることがあります。`resource.subjects`を`List`として扱う式や、`app.name + "." + namespace`のように`Any`値をStringへ連結する式は、実際の値が期待する型でも静的型推論が入力の型を特定できず、型付き評価器へ渡せないことがありました。importした型aliasのList要素も、alias定義元のscopeを再帰的な型検査へ引き継がない場合がありました。

## 採用した設計

静的型推論は`Any`値をList操作や`+`演算の候補として受け入れ、式を型付き評価器へ渡します。実行時は`lists.map`などの組み込み関数が入力値を検査し、String連結は左右の値を検査します。静的型が不確かな値をtyped functionへ渡すときは、宣言された引数型を実行時に検査します。既知の具体型どうしの不一致は引き続き静的に拒否します。

optional record fieldは静的型で`T | Null`として扱います。値を読むときは欠けたoptional fieldを`Null`へ変換し、`??`のfallbackを評価できます。Map overlayはその式を含むpatchをLoweringできるようにします。

importされた公開aliasの構造は定義元の型表で解決し、入れ子のfield、List要素、Map要素へ型情報を引き継ぎます。これにより、`Configuration.settings`や`Settings`の`List<RackSettings>`を静的推論と実行時検査の両方で解決できます。

inline lambdaは定義元の型表とModuleContextを保持し、呼び出し元の同名aliasで引数の型を検査しません。privateなaliasを使用する公開値や関数も、そのaliasを公開名へ追加せずに検査できます。再importでキャッシュを使う際も、定義元の完全な型表を公開aliasだけの型表で上書きしません。

`optionalList ?? []`と`optionalMap ?? {}`では、空のfallbackを左辺の非Null型に照らして型付けします。空のList/Mapの要素型が未確定であることを理由に、関数本体全体を未対応の式として拒否しません。

Parameter JSONから作られたRuntimeValueはソース抜粋を抑止するmetadataを持ちます。型検査とJSON Schema検査はmetadataを診断へ渡し、CLIはファイル名・行・列を表示しながら入力元のソース行を省略します。

## 検証

回帰テストでoptional field、空List/Mapのfallback、`Any`のList操作とString連結、入れ子alias、private aliasのshadowing、diamond import、parameter JSON診断のsource snippet抑止を確認しました。
`go test ./...`、`golangci-lint run ./...`が成功しました。
infraで型付き評価に失敗していた29配置について、開発版CLIと通常のSchema Catalogを使って再評価し、すべて成功しました。Catalogなしの切り分けは原因調査にのみ使用し、配置の評価成功として数えていません。

## 公開バージョン

MCL Release workflow完了後に公開バージョンを記録します。
