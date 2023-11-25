package main

// ISUCON的な参考: https://github.com/isucon/isucon12-qualify/blob/main/webapp/go/isuports.go#L336
// sqlx的な参考: https://jmoiron.github.io/sqlx/

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/motoki317/sc"

	"github.com/gorilla/sessions"
	"github.com/kaz/pprotein/integration/standalone"
	"github.com/labstack/echo-contrib/session"
	echolog "github.com/labstack/gommon/log"
)

const (
	listenPort                     = 8080
	powerDNSSubdomainAddressEnvKey = "ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS"
)

var (
	powerDNSSubdomainAddress string
	dbConn                   *sqlx.DB
	secret                   = []byte("isucon13_session_cookiestore_defaultsecret")
)

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	if secretKey, ok := os.LookupEnv("ISUCON13_SESSION_SECRETKEY"); ok {
		secret = []byte(secretKey)
	}
}

type InitializeResponse struct {
	Language string `json:"language"`
}

func stmtClose(stmt *sqlx.Stmt) {
	_ = stmt.Close()
}

var stmtCache = sc.NewMust(func(ctx context.Context, query string) (*sqlx.Stmt, error) {
	stmt, err := dbConn.PreparexContext(ctx, query)
	if err != nil {
		return nil, err
	}
	runtime.SetFinalizer(stmt, stmtClose)
	return stmt, nil
}, 90*time.Second, 90*time.Second)

func dbExec(query string, args ...any) (sql.Result, error) {
	stmt, err := stmtCache.Get(context.Background(), query)
	if err != nil {
		return nil, err
	}
	return stmt.Exec(args...)
}

func dbGet(dest interface{}, query string, args ...interface{}) error {
	stmt, err := stmtCache.Get(context.Background(), query)
	if err != nil {
		return err
	}
	return stmt.Get(dest, args...)
}

func dbSelect(dest interface{}, query string, args ...interface{}) error {
	stmt, err := stmtCache.Get(context.Background(), query)
	if err != nil {
		return err
	}
	return stmt.Select(dest, args...)
}

func txExec(tx *sqlx.Tx, query string, args ...any) (sql.Result, error) {
	stmt, err := stmtCache.Get(context.Background(), query)
	if err != nil {
		return nil, err
	}
	return tx.Stmtx(stmt).Exec(args...)
}

func txGet(tx *sqlx.Tx, dest interface{}, query string, args ...interface{}) error {
	stmt, err := stmtCache.Get(context.Background(), query)
	if err != nil {
		return err
	}
	return tx.Stmtx(stmt).Get(dest, args...)
}

func txSelect(tx *sqlx.Tx, dest interface{}, query string, args ...interface{}) error {
	stmt, err := stmtCache.Get(context.Background(), query)
	if err != nil {
		return err
	}
	return tx.Stmtx(stmt).Select(dest, args...)
}

func connectDB(logger echo.Logger) (*sqlx.DB, error) {
	const (
		networkTypeEnvKey = "ISUCON13_MYSQL_DIALCONFIG_NET"
		addrEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_ADDRESS"
		portEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_PORT"
		userEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_USER"
		passwordEnvKey    = "ISUCON13_MYSQL_DIALCONFIG_PASSWORD"
		dbNameEnvKey      = "ISUCON13_MYSQL_DIALCONFIG_DATABASE"
		parseTimeEnvKey   = "ISUCON13_MYSQL_DIALCONFIG_PARSETIME"
	)

	conf := mysql.NewConfig()

	// 環境変数がセットされていなかった場合でも一旦動かせるように、デフォルト値を入れておく
	// この挙動を変更して、エラーを出すようにしてもいいかもしれない
	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort("127.0.0.1", "3306")
	conf.User = "isucon"
	conf.Passwd = "isucon"
	conf.DBName = "isupipe"
	conf.ParseTime = true
	conf.InterpolateParams = true

	if v, ok := os.LookupEnv(networkTypeEnvKey); ok {
		conf.Net = v
	}
	if addr, ok := os.LookupEnv(addrEnvKey); ok {
		if port, ok2 := os.LookupEnv(portEnvKey); ok2 {
			conf.Addr = net.JoinHostPort(addr, port)
		} else {
			conf.Addr = net.JoinHostPort(addr, "3306")
		}
	}
	if v, ok := os.LookupEnv(userEnvKey); ok {
		conf.User = v
	}
	if v, ok := os.LookupEnv(passwordEnvKey); ok {
		conf.Passwd = v
	}
	if v, ok := os.LookupEnv(dbNameEnvKey); ok {
		conf.DBName = v
	}
	if v, ok := os.LookupEnv(parseTimeEnvKey); ok {
		parseTime, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("failed to parse environment variable '%s' as bool: %+v", parseTimeEnvKey, err)
		}
		conf.ParseTime = parseTime
	}

	db, err := sqlx.Open("mysql", conf.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1024)
	db.SetMaxIdleConns(-1)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

func initializeHandler(c echo.Context) error {
	go func() {
		if _, err := http.Get("http://p.isucon.ikura-hamu.work/api/group/collect"); err != nil {
			log.Printf("failed to communicate with pprotein: %v", err)
		}
	}()
	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		c.Logger().Warnf("init.sh failed with err=%s", string(out))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to initialize: "+err.Error())
	}

	/*
		ALTER TABLE `isupipe`.`livestream_tags` ADD INDEX `livestream_id` (`livestream_id`);
		ALTER TABLE `isupipe`.`icons` ADD INDEX `user_id` (`user_id`);
		ALTER TABLE `isupipe`.`livecomments` ADD INDEX `livestream_id` (`livestream_id`);
		ALTER TABLE `isupipe`.`ng_words` ADD INDEX `user_id_livestream_id` (`user_id`, `livestream_id`);
		ALTER TABLE `isudns`.`records` ADD INDEX (name);
		ALTER TABLE `isupipe`.`themes` ADD INDEX `user_id` (`user_id`);
		ALTER TABLE `isupipe`.`reservation_slots` ADD INDEX `start_at_end_at` (`start_at`, `end_at`);
	*/
	dbConn.Exec("ALTER TABLE `isupipe`.`livestream_tags` ADD INDEX `livestream_id` (`livestream_id`);")
	dbConn.Exec("ALTER TABLE `isupipe`.`icons` ADD INDEX `user_id` (`user_id`);")
	dbConn.Exec("ALTER TABLE `isupipe`.`livecomments` ADD INDEX `livestream_id` (`livestream_id`);")
	dbConn.Exec("ALTER TABLE `isupipe`.`ng_words` ADD INDEX `user_id_livestream_id` (`user_id`, `livestream_id`);")
	dbConn.Exec("ALTER TABLE `isudns`.`records` ADD INDEX `name` (`name`);")
	dbConn.Exec("ALTER TABLE `isupipe`.`themes` ADD INDEX `user_id` (`user_id`);")
	dbConn.Exec("ALTER TABLE `isupipe`.`reservation_slots` ADD INDEX `start_at_end_at` (`start_at`, `end_at`);")

	c.Request().Header.Add("Content-Type", "application/json;charset=utf-8")
	return c.JSON(http.StatusOK, InitializeResponse{
		Language: "golang",
	})
}

var tagCacheByName *sc.Cache[string, *TagModel]

func getTagByName(_ context.Context, tagName string) (*TagModel, error) {
	var tag TagModel
	err := dbConn.Get(&tag, "SELECT * FROM tags WHERE name = ?", tagName)
	if err != nil {
		return nil, err
	}
	return &tag, nil
}

var tagCacheByID *sc.Cache[int64, *TagModel]

func getTagByID(_ context.Context, tagID int64) (*TagModel, error) {
	var tag TagModel
	err := dbConn.Get(&tag, "SELECT * FROM tags WHERE id = ?", tagID)
	if err != nil {
		return nil, err
	}
	return &tag, nil
}

var tagsCache *sc.Cache[struct{}, []*TagModel]

func getTags(_ context.Context, _ struct{}) ([]*TagModel, error) {
	var tags []*TagModel
	err := dbConn.Select(&tags, "SELECT * FROM tags")
	if err != nil {
		return nil, err
	}
	return tags, nil
}

func main() {
	go standalone.Integrate(":8888")

	tagCacheByID = sc.NewMust[int64, *TagModel](getTagByID, time.Minute, time.Minute, sc.With2QBackend(150))
	tagCacheByName = sc.NewMust[string, *TagModel](getTagByName, time.Minute, time.Minute, sc.With2QBackend(150))
	tagsCache = sc.NewMust[struct{}, []*TagModel](getTags, time.Minute, time.Minute, sc.With2QBackend(1))

	e := echo.New()
	e.Debug = true
	e.Logger.SetLevel(echolog.DEBUG)
	e.Use(middleware.Logger())
	cookieStore := sessions.NewCookieStore(secret)
	cookieStore.Options.Domain = "*.u.isucon.dev"
	e.Use(session.Middleware(cookieStore))
	// e.Use(middleware.Recover())

	// 初期化
	e.POST("/api/initialize", initializeHandler)

	// top
	e.GET("/api/tag", getTagHandler)
	e.GET("/api/user/:username/theme", getStreamerThemeHandler)

	// livestream
	// reserve livestream
	e.POST("/api/livestream/reservation", reserveLivestreamHandler)
	// list livestream
	e.GET("/api/livestream/search", searchLivestreamsHandler)
	e.GET("/api/livestream", getMyLivestreamsHandler)
	e.GET("/api/user/:username/livestream", getUserLivestreamsHandler)
	// get livestream
	e.GET("/api/livestream/:livestream_id", getLivestreamHandler)
	// get polling livecomment timeline
	e.GET("/api/livestream/:livestream_id/livecomment", getLivecommentsHandler)
	// ライブコメント投稿
	e.POST("/api/livestream/:livestream_id/livecomment", postLivecommentHandler)
	e.POST("/api/livestream/:livestream_id/reaction", postReactionHandler)
	e.GET("/api/livestream/:livestream_id/reaction", getReactionsHandler)

	// (配信者向け)ライブコメントの報告一覧取得API
	e.GET("/api/livestream/:livestream_id/report", getLivecommentReportsHandler)
	e.GET("/api/livestream/:livestream_id/ngwords", getNgwords)
	// ライブコメント報告
	e.POST("/api/livestream/:livestream_id/livecomment/:livecomment_id/report", reportLivecommentHandler)
	// 配信者によるモデレーション (NGワード登録)
	e.POST("/api/livestream/:livestream_id/moderate", moderateHandler)

	// livestream_viewersにINSERTするため必要
	// ユーザ視聴開始 (viewer)
	e.POST("/api/livestream/:livestream_id/enter", enterLivestreamHandler)
	// ユーザ視聴終了 (viewer)
	e.DELETE("/api/livestream/:livestream_id/exit", exitLivestreamHandler)

	// user
	e.POST("/api/register", registerHandler)
	e.POST("/api/login", loginHandler)
	e.GET("/api/user/me", getMeHandler)
	// フロントエンドで、配信予約のコラボレーターを指定する際に必要
	e.GET("/api/user/:username", getUserHandler)
	e.GET("/api/user/:username/statistics", getUserStatisticsHandler)
	e.GET("/api/user/:username/icon", getIconHandler)
	e.POST("/api/icon", postIconHandler)

	// stats
	// ライブ配信統計情報
	e.GET("/api/livestream/:livestream_id/statistics", getLivestreamStatisticsHandler)

	// 課金情報
	e.GET("/api/payment", GetPaymentResult)

	e.HTTPErrorHandler = errorResponseHandler

	// DB接続
	conn, err := connectDB(e.Logger)
	if err != nil {
		e.Logger.Errorf("failed to connect db: %v", err)
		os.Exit(1)
	}
	defer conn.Close()
	dbConn = conn

	http.DefaultTransport.(*http.Transport).MaxIdleConns = 0           // infinite
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = 1024 // default: 2
	//http.DefaultTransport.(*http.Transport).ForceAttemptHTTP2 = true   // go1.13以上

	subdomainAddr, ok := os.LookupEnv(powerDNSSubdomainAddressEnvKey)
	if !ok {
		e.Logger.Errorf("environ %s must be provided", powerDNSSubdomainAddressEnvKey)
		os.Exit(1)
	}
	powerDNSSubdomainAddress = subdomainAddr

	if os.Getenv("USE_SOCKET") == "1" {
		// ここからソケット接続設定 ---
		socket_file := "/tmp/app.sock"
		os.Remove(socket_file)

		l, err := net.Listen("unix", socket_file)
		if err != nil {
			e.Logger.Fatal(err)
		}

		// go runユーザとnginxのユーザ（グループ）を同じにすれば777じゃなくてok
		err = os.Chmod(socket_file, 0777)
		if err != nil {
			e.Logger.Fatal(err)
		}

		e.Listener = l
		e.Logger.Fatal(e.Start(""))
		// ここまで ---
	} else {
		// HTTPサーバ起動
		listenAddr := net.JoinHostPort("", strconv.Itoa(listenPort))
		if err := e.Start(listenAddr); err != nil {
			e.Logger.Errorf("failed to start HTTP server: %v", err)
			os.Exit(1)
		}
	}
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func errorResponseHandler(err error, c echo.Context) {
	c.Logger().Errorf("error at %s: %+v", c.Path(), err)
	if he, ok := err.(*echo.HTTPError); ok {
		if e := c.JSON(he.Code, &ErrorResponse{Error: err.Error()}); e != nil {
			c.Logger().Errorf("%+v", e)
		}
		return
	}

	if e := c.JSON(http.StatusInternalServerError, &ErrorResponse{Error: err.Error()}); e != nil {
		c.Logger().Errorf("%+v", e)
	}
}
