// mod turbo_grpc;
// use turbo_grpc::Turbo;
use anyhow::Result;
use tonic::{transport::Server, Request, Response, Status};

use turbo::turbo_server::{Turbo, TurboServer};
use turbo::{GlobalHashReply, GlobalHashRequest};

pub mod turbo {
    tonic::include_proto!("daemon");
}

#[no_mangle]
pub extern "C" fn run() -> i32 {
    match run_server() {
      Err(e) => {
        println!("got error {:?}", e);
        1
      }
      Ok(_) => 0
    }
}

pub fn run_server() -> Result<()> {
  let rt = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()?;

  let addr = "127.0.0.1:5555".parse()?;
  rt.block_on(async move {
    let turbod = Turbod {};
    let result = Server::builder().add_service(TurboServer::new(turbod)).serve(addr).await;
    match result {
      Ok(_) => Ok(()),
      Err(e) => {
        println!("Err {:?}", e);
        Ok(())
      }
    }
  })
  //Ok(())
}

struct Turbod {}

#[tonic::async_trait]
impl Turbo for Turbod {
    async fn get_global_hash(
        &self,
        req: Request<GlobalHashRequest>,
    ) -> Result<Response<GlobalHashReply>, Status> {
        let f = "foo!";
        let reply = GlobalHashReply {
            hash: f.as_bytes().to_vec().clone(),
        };
        Ok(Response::new(reply))
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn it_works() {
        let result = 2 + 2;
        assert_eq!(result, 4);
    }
}
