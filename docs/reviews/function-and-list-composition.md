# 関数とListの合成

## 対象と設計

infraのAkvoradoではContainerの構築に5つの位置引数を渡し、未使用のvolumeMountsにも空Listの変数を用意していました。SeaweedFSでは同じClientのIdentity、Policy、Binding、Credentialsを別々に走査していました。関数の引数に名前を付け、省略可能な設定にデフォルト値を持たせ、複数のListを明示的に展開することで、設定の意味とResourceの対応関係を読み取りやすくします。

関数呼び出しは`foo(...)`、名前付き引数は`name = value`、Mapのフィールドは`name: value`を使います。名前付き引数の構文と引数の並べ替えは既存実装を利用し、デフォルト引数と組み合わせて省略を可能にします。`foo:`による呼び出し、暗黙のReceiver、専用のManifest構文は追加しません。

## 関数引数

引数は`fn container(name: String, mounts: List<String> = [])`のように宣言します。デフォルト値のある引数の後にも必須引数を宣言できます。位置引数は宣言順に対応し、名前付き引数は宣言名で対応します。名前付き引数の後に位置引数を書くこと、同じ引数を2回指定すること、未知の名前を指定すること、必須引数を省略することはエラーです。

省略した引数のデフォルト式は呼び出し時に関数の定義環境で評価し、それより前の引数を参照できます。明示的に渡した値はデフォルト式より優先します。後続の引数や呼び出し元のローカル変数への暗黙の参照は認めません。デフォルト式も宣言した引数型を満たす必要があります。

## 関数宣言の順序

トップレベル関数のシグネチャを本文より先に収集し、後に宣言した関数を呼び出せるようにします。公開する関数を先頭に、内部の補助関数を後方に配置できます。通常の`let`の前方参照を許可する変更は含めません。関数同士の呼び出しでも既存の評価予算と再帰制限を適用します。

## Listの展開

`[first, ...items, last]`は`items`の要素をその位置に順番どおり挿入します。展開する値はListである必要があります。空Listは0要素を挿入し、ネストしたListは1段だけ展開します。通常のList要素、`for`の本文、`emit`には暗黙のflattenを追加しません。複数Resourceの出力には引き続き`emit each`を使います。

型検査では展開元のList要素型と、展開先のList要素の期待型を照合します。実行時の型が必要な`Any`に対しても、List以外の値を展開できないことを検証します。展開後の要素数と評価処理にも既存の割当・評価予算を適用します。

既存の`std:list`には`map`と`flatten`がありますが、ClientごとのResource Listを直接合成する`flatMap`はありませんでした。`lists.flatMap(items, fn(item) => resources(item))`を追加し、各コールバックの戻り値をListとして検証して1段だけ展開します。入力とコールバック内の要素順序を保持し、出力要素型はコールバックの戻り値のList要素型から推論します。

## Formatter

インデントListのMap要素は`- name: "http"`のように先頭フィールドをハイフンと同じ行に置きます。後続フィールドは先頭フィールドと揃えます。コメント、空Map、ネストしたブロックなどのため同じ行に置けない場合は、意味を保持する表記を選びます。整形後の再parse、要素順序、2回整形した結果の一致を確認します。

## 型付きレコードとnode

大きな設定には既存の型付きレコードとインデントMapを使い、短い設定には名前付き引数を使います。型フィールドへのデフォルト値は関数引数とは異なり、Map構築、型変換、型境界を越える値への補完規則が必要なため、関数引数の変更には含めません。

複数行のインラインMapを関数本文として返すと、継続行の検出が丸括弧しか数えず、`unmatched opening brace`で失敗していました。継続行の判定では`()`、`[]`、`{}`を扱い、関数呼び出し内だけでなく、通常の式でも同じMap・List構文を使用可能にします。

Cluster APIの設定合成では、`identity()`の戻り値を`Map<String, Any>`と宣言すると、`identity() with: ...`から閉じた設定レコードへの型検査に必要なフィールド情報が失われます。この挙動はv0.3.32でも再現します。関数の戻り値型にないフィールドを呼び出し元で推測する変更は行わず、infraでは`patches`、`schematicID`、`talosVersion`、`hostSelectorLabels`を持つ共有の`MachineIdentity`型を定義します。identity関数はそのレコードを返し、ControlPlaneConfigとWorkerConfigへ残りのフィールドを明示的に追加します。identityはハッシュと設定の入力なので通常のMapとして構築し、独立したResourceの同一性を付与する`node:`は使いません。

`node:`の名前と構文は維持します。参照可能な同一性が必要な値と通常のMapを区別する設計は、構文の置換と分けて検討します。

## infraへの適用

AkvoradoではContainerの呼び出しを名前付き引数へ変更し、`mounts`を省略可能にします。ラベルとContainerの構築関数は通常のMapを返し、独立して出力するDeployment、Service、ServiceMonitor、ConfigMapには`node:`を使います。

SeaweedFSでは1つのS3 ClientからIdentity、Policy、Binding、Credentialsを生成し、`flatMap`で各ClientのResourceをまとめます。BucketのListとはSpreadで合成します。Resource内容を維持し、出力順序はClientごとのまとまりになります。

Picca、PostgreSQL、prfexample、Walnukでは、単一Resourceを1要素Listに包む補助関数や、何段にもネストした`lists.concat()`を削除します。通常のList要素と明示的なSpreadを組み合わせ、単純な射影には`for`を使います。PostgreSQLの公開関数は内部の補助関数より前に配置します。

関数宣言の先行収集により、oekaki-dengon-gameのテンプレート変数と構築関数が同じ`frontDeployment`という名前を持っていたことが検出されます。テンプレート変数を`frontDeploymentTemplate`へ改名し、モジュール内の名前を一意にします。
