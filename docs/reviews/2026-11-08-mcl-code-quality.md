# 2026年11月8日 MCLコード品質・性能レビュー

## 対象

`d2e7e50`を基準に、レビューで挙げられたSource Map、Parser、CRD判定、Formatter、依存Module index、Typed IR解析キャッシュ、Typed IR評価器、CLIフラグ、Schema補完、Dependency Lockを確認します。実装時のHEADは`beceed2`です。言語仕様、レンダリング結果、診断内容と順序、公開API、再現性の挙動は変更対象にしません。

## 実装

Source Map生成では、YAML nodeごとの出力範囲をdocument単位で一度計算し、Manifest値と子要素の対応付けから再利用します。行末offsetも出力全体から一度計算し、nodeごとの改行検索を除きます。親nodeの出力範囲は、従来どおり自身と子孫の終端offsetの最大値です。

CRD Schema Catalogが必要か判定する経路では、組み込みGVKの存在確認に`Builtin()`を使わず、型構築を行わない`HasBuiltinSchema()`を使います。Catalogの読み込み条件と評価結果は変わりません。

FormatterのList内Map開始位置を整える後処理では、置換範囲を元sourceに対して昇順に処理し、`strings.Builder`へ一度だけ出力します。置換ごとのsource全体の再構築をなくし、置換内容と改行を維持します。

通常Schema補完、Schema型フィールド列挙、Helm Schema補完で重複していたプロパティ収集・既存候補の除外・ソートを`schemaCompletionPropertyNames()`にまとめます。呼び出し元ごとの既存フィールド除外と候補順を維持します。

Parserでは、トップレベルの宣言順序判定と、`type`・`param`宣言のトークン解析およびAST構築を分離します。トップレベルにはフェーズ遷移とASTへの追加を残し、各宣言の解析関数は元の診断文、Source Span、parser helperの呼び出し順を維持します。

Language Serviceのproject FS経路では、読み込んだsource一式を`ProjectSemanticSnapshot`に保持し、各モジュールのASTと`SemanticIndex`を必要になった時点で一度だけ生成します。Definition、Hover、References、Rename、名前付き引数補完は、同じsnapshot内の解析結果を再利用します。構文エラーのあるsourceもsnapshotのsource一覧には保持し、機能ごとの既存の失敗・スキップ動作を維持します。現状ではproject FS APIの呼び出しごとにsnapshotを作成するため、LSPリクエスト間でSemanticIndexを再利用する段階には至っていません。

## 今回変更しない項目

Dependency Module indexの差分更新は、import解決がファイル探索結果にも依存するため、source hashだけで再解決を省く設計にはしません。現状は重複処理を共通化する余地を追加調査し、依存解決のキャッシュキーと無効化条件が明確になるまで挙動を変更しません。

Typed IR解析結果のコピーは、解析結果内の型・Facts・FunctionInferenceが複数箇所で変更されるため、所有権と変更箇所を追跡せずに共有へ切り替えません。評価器の巨大なswitchも、遅延評価・Source Location・スコープ捕捉を保つ責務境界を確認してから分割します。

CLIの共通フラグは、コマンドごとに対象パラメータや必要なoptionが異なります。共通化によってCobraのhelp表示、default、flag errorの発生箇所が変わらない単位を確認できるまで、コマンド登録を変更しません。

Dependency Lockの3モードは、キャッシュ破損時の回復動作、外部取得、エラーの型と文面が異なります。モード差を保ったまま重複が減るヘルパーを特定できていないため、今回のコード変更には含めません。

## 検証

`go test ./internal/syntax`、`go test ./internal/engine -count=1`、`go test ./...`は成功しました。`mise run quality:check`はformat、WASM確認、testを通過しましたが、HEADの別変更にあるgoconst 3件とstaticcheck 1件でlintが失敗しました。性能改善幅は専用benchmarkを比較するまで数値として主張しません。

## 状態

Source Map、CRD判定、Formatter、Schema補完、Parserの`type`・`param`宣言を実装しました。Language Serviceには遅延解析snapshotを追加し、同一問い合わせ内のASTとSemanticIndexの再解析を除きました。Dependency Module indexの差分更新、LSPリクエスト間のSemanticIndex共有、Typed IR解析キャッシュ、Typed IR評価器、CLIフラグ、Dependency Lockは、既存の無効化条件・所有権・診断・コマンドごとの挙動を保つ具体的な変更単位を確認できていないため、引き続き未対応です。
