use std::ffi::CString;
use std::os::raw::c_char;
#[no_mangle]
pub extern "C" fn get_global_hash() -> *mut c_char {
  let val = CString::new("foo").unwrap();
  val.into_raw()
}

#[no_mangle]
pub unsafe extern "C" fn deallocate_global_hash(ptr: *mut c_char) {
  drop(CString::from_raw(ptr))
}

#[cfg(test)]
mod tests {
    #[test]
    fn it_works() {
        let result = 2 + 2;
        assert_eq!(result, 4);
    }
}
