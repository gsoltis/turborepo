fn main() -> Result<(), Box<dyn std::error::Error>> {
    // protoc_rust_grpc::Codegen::new()
    //     .out_dir("src")
    //     .includes(&["../../cli/internal/daemon"])
    //     .input("../../cli/internal/daemon/turbo.proto")
    //     .rust_protobuf(true)
    //     .run()
    //     .expect("error compiling protobuf")
    tonic_build::compile_protos("../../cli/internal/daemon/turbo.proto")?;
    Ok(())
}
