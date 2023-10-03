use anyhow::{anyhow, Result};
use napi_derive::napi;
use turbopath::AbsoluteSystemPathBuf;
use turborepo_repository::{
    inference::{RepoMode, RepoState},
    package_manager::{self, PackageManager as RustPackageManager},
};

#[napi]
pub struct Repository {
    repo_state: RepoState,
    pub root: String,
    pub is_monorepo: bool,
}

#[napi]
pub struct PackageManager {
    package_manager: RustPackageManager,
    pub name: String,
}

impl From<RustPackageManager> for PackageManager {
    fn from(package_manager: RustPackageManager) -> Self {
        Self {
            name: package_manager.to_string(),
            package_manager,
        }
    }
}

#[napi]
impl Repository {
    #[napi(factory, js_name = "detectJS")]
    pub fn detect_js(path: Option<String>) -> Result<Self> {
        let reference_dir = path
            .map(|path| {
                AbsoluteSystemPathBuf::from_cwd(&path)
                    .map_err(|e| anyhow!("Couldn't resolve path {}: {}", path, e))
            })
            .unwrap_or_else(|| {
                AbsoluteSystemPathBuf::cwd()
                    .map_err(|e| anyhow!("Couldn't resolve path from cwd: {}", e))
            })?;
        let repo_state = RepoState::infer(&reference_dir).map_err(|e| anyhow!(e))?;
        let is_monorepo = repo_state.mode == RepoMode::MultiPackage;
        Ok(Self {
            root: repo_state.root.to_string(),
            repo_state,
            is_monorepo,
        })
    }

    #[napi]
    pub fn package_manager(&self) -> Result<PackageManager> {
        // match rather than map/map_err due to only the Ok variant implementing "Copy"
        // match lets us handle each case independently, rather than forcing the whole
        // value to a reference or concrete value
        match self.repo_state.package_manager {
            Ok(pm) => Ok(pm.into()),
            Err(ref e) => Err(anyhow!("{}", e)),
        }
    }
}
