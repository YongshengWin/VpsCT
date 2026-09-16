"""Candidate-only syscall filter. Not a production launcher."""
import ctypes, os, socket, sys

def restrict_raw():
    lib=ctypes.CDLL('libseccomp.so.2',use_errno=True)
    class Arg(ctypes.Structure):
        _fields_=[('arg',ctypes.c_uint),('op',ctypes.c_int),('a',ctypes.c_uint64),('b',ctypes.c_uint64)]
    lib.seccomp_init.argtypes=[ctypes.c_uint32];lib.seccomp_init.restype=ctypes.c_void_p
    lib.seccomp_syscall_resolve_name.argtypes=[ctypes.c_char_p];lib.seccomp_syscall_resolve_name.restype=ctypes.c_int
    lib.seccomp_rule_add_array.argtypes=[ctypes.c_void_p,ctypes.c_uint32,ctypes.c_int,ctypes.c_uint,ctypes.POINTER(Arg)]
    lib.seccomp_load.argtypes=[ctypes.c_void_p];lib.seccomp_release.argtypes=[ctypes.c_void_p]
    ctx=lib.seccomp_init(0x7fff0000)
    if not ctx:raise RuntimeError('seccomp unavailable')
    try:
        nr=lib.seccomp_syscall_resolve_name(b'socket')
        for family in [socket.AF_INET,socket.AF_INET6]:
            args=(Arg*2)(Arg(0,4,family,0),Arg(1,7,15,socket.SOCK_RAW))
            assert lib.seccomp_rule_add_array(ctx,0x50001,nr,2,args)==0
        args=(Arg*1)(Arg(0,4,socket.AF_PACKET,0))
        assert lib.seccomp_rule_add_array(ctx,0x50001,nr,1,args)==0
        assert lib.seccomp_load(ctx)==0
    finally:lib.seccomp_release(ctx)

if __name__=='__main__':
    restrict_raw();os.execv(sys.argv[1],sys.argv[1:])
