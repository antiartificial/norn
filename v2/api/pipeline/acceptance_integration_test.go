package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/model"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

func acceptancePipelineFixture(t *testing.T) (*Pipeline, *store.DB, EnqueueRequest) {
	t.Helper()
	databaseURL:=os.Getenv("NORN_TEST_DATABASE_URL");if databaseURL==""{t.Skip("NORN_TEST_DATABASE_URL is not set")}
	ctx:=context.Background();admin,err:=pgxpool.New(ctx,databaseURL);if err!=nil{t.Fatal(err)}
	schemaName:="norn_pipeline_acceptance_"+strings.ReplaceAll(uuid.NewString(),"-","");identifier:=pgx.Identifier{schemaName}.Sanitize()
	if _,err:=admin.Exec(ctx,`CREATE SCHEMA `+identifier);err!=nil{t.Fatal(err)}
	config,err:=pgxpool.ParseConfig(databaseURL);if err!=nil{t.Fatal(err)};config.ConnConfig.RuntimeParams["search_path"]=schemaName
	pool,err:=pgxpool.NewWithConfig(ctx,config);if err!=nil{t.Fatal(err)}
	t.Cleanup(func(){pool.Close();_,_ = admin.Exec(context.Background(),`DROP SCHEMA `+identifier+` CASCADE`);admin.Close()})
	db:=&store.DB{Pool:pool};if err:=store.Migrate(db);err!=nil{t.Fatal(err)}
	signer,err:=store.NewHMACAcceptanceSigner("pipeline-acceptance-test-signing-key-000000000");if err!=nil{t.Fatal(err)}
	opStore,err:=store.NewPGOperationStore(db,signer,store.AcceptancePolicy{});if err!=nil{t.Fatal(err)}
	authority,err:=opStore.Authority(ctx);if err!=nil{t.Fatal(err)}
	p:=&Pipeline{DB:db,SagaStore:saga.NewPostgresStore(pool),OperationStore:opStore}
	request:=EnqueueRequest{Authority:authority,Actor:store.OperationActor{Issuer:authority+"/test",Subject:"operator"},Key:"request-key",Audit:store.AcceptanceAuditContext{Source:"pipeline-integration"}}
	return p,db,request
}

func TestPipelineAcceptanceReplayRollbackAndTransactionalFailure(t *testing.T){
	p,db,request:=acceptancePipelineFixture(t);ctx:=context.Background();spec:=&model.InfraSpec{App:"demo",Deploy:true,Processes:map[string]model.Process{}}
	first,err:=p.Run(ctx,spec,"main",request);if err!=nil{t.Fatal(err)}
	second,err:=p.Run(ctx,spec,"main",request);if err!=nil{t.Fatal(err)}
	if !second.Replayed||first.Operation.ID!=second.Operation.ID||first.Intent.DeploymentID!=second.Intent.DeploymentID{t.Fatalf("deploy replay diverged first=%+v second=%+v",first,second)}
	var operations,deployments,intents int
	if err:=db.Pool.QueryRow(ctx,`SELECT (SELECT count(*) FROM operations),(SELECT count(*) FROM deployments),(SELECT count(*) FROM operation_acceptance_intents)`).Scan(&operations,&deployments,&intents);err!=nil{t.Fatal(err)}
	if operations!=1||deployments!=1||intents!=1{t.Fatalf("replay rows op=%d deploy=%d intent=%d",operations,deployments,intents)}

	bad:=request;bad.Key="missing-receipt";bad.Audit.RequestReceiptID=uuid.NewString()
	if _,err:=p.Run(ctx,&model.InfraSpec{App:"rollback-proof",Deploy:true},"main",bad);err==nil{t.Fatal("missing receipt acceptance succeeded")}
	if err:=db.Pool.QueryRow(ctx,`SELECT (SELECT count(*) FROM operations WHERE app='rollback-proof')+(SELECT count(*) FROM deployments WHERE app='rollback-proof')`).Scan(&operations);err!=nil{t.Fatal(err)}
	if operations!=0{t.Fatalf("failed intent persistence left %d domain rows",operations)}

	current:=model.Deployment{ID:uuid.NewString(),App:"rollback-app",SagaID:uuid.NewString(),Environment:"staging",Status:model.StatusDeployed,StartedAt:time.Now()}
	previous:=&model.Deployment{ID:uuid.NewString(),App:"rollback-app",SagaID:uuid.NewString(),CommitSHA:"0123456789abcdef0123456789abcdef01234567",ImageTag:"registry/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",Environment:"staging",Status:model.StatusDeployed,StartedAt:time.Now()}
	rollbackRequest:=request;rollbackRequest.Key="rollback-action";rollbackRequest.Semantics=map[string]interface{}{"currentDeploymentId":current.ID,"sourceDeploymentId":previous.ID}
	rollbackSpec:=&model.InfraSpec{App:"rollback-app",Deploy:true}
	rolled,err:=p.QueueRollback(ctx,rollbackSpec,current,previous,nil,rollbackRequest,nil);if err!=nil{t.Fatal(err)}
	replayed,err:=p.QueueRollback(ctx,rollbackSpec,current,previous,nil,rollbackRequest,nil);if err!=nil{t.Fatal(err)}
	if !replayed.Replayed||replayed.Operation.ID!=rolled.Operation.ID||replayed.Intent.DeploymentID!=rolled.Intent.DeploymentID{t.Fatalf("rollback replay diverged: %+v %+v",rolled,replayed)}
}

func TestDeployGroupParentManifestRejectsMembershipChange(t *testing.T){
	p,_,request:=acceptancePipelineFixture(t);appsDir:=t.TempDir();appDir:=filepath.Join(appsDir,"app-a");if err:=os.MkdirAll(appDir,0700);err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(filepath.Join(appDir,"infraspec.yaml"),[]byte("name: app-a\ndeploy: true\nprocesses: {}\n"),0600);err!=nil{t.Fatal(err)}
	first:=&model.DeployGroup{Name:"core",Apps:[]model.DeployGroupApp{{App:"app-a"}}}
	queued,err:=p.RunGroup(context.Background(),first,"main",appsDir,request);if err!=nil{t.Fatal(err)}
	if len(queued.Deploys)!=1||queued.Deploys[0].OperationID==""{t.Fatalf("group queue=%+v",queued)}
	changed:=&model.DeployGroup{Name:"core",Apps:[]model.DeployGroupApp{{App:"app-a"},{App:"app-b"}}}
	if _,err:=p.RunGroup(context.Background(),changed,"main",appsDir,request);!errors.Is(err,store.ErrAcceptanceConflict){t.Fatalf("changed group membership error=%v",err)}
	resolved,err:=p.RunGroup(context.Background(),first,"main",appsDir,request);if err!=nil{t.Fatal(err)}
	if !resolved.Replayed||resolved.OperationID!=queued.OperationID||resolved.Deploys[0].OperationID!=queued.Deploys[0].OperationID{t.Fatalf("group replay diverged: %+v %+v",queued,resolved)}
}
