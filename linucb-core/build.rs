//! Generate the C header (`include/linucb.h`) from the FFI surface so the Go
//! proxy can include it via CGO.

use std::env;
use std::path::PathBuf;

fn main() {
    let crate_dir = env::var("CARGO_MANIFEST_DIR").unwrap();
    let out = PathBuf::from(&crate_dir).join("include").join("linucb.h");

    // Best-effort: don't fail the build if header generation hiccups (e.g. in a
    // minimal CI without the config); the committed header remains the source.
    if let Ok(bindings) = cbindgen::generate(&crate_dir) {
        std::fs::create_dir_all(out.parent().unwrap()).ok();
        bindings.write_to_file(&out);
    }

    println!("cargo:rerun-if-changed=src/ffi.rs");
    println!("cargo:rerun-if-changed=cbindgen.toml");
}
