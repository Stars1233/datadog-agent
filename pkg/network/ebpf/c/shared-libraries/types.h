#ifndef __SHARED_LIBRARIES_TYPES_H
#define __SHARED_LIBRARIES_TYPES_H

#include "ktypes.h"

#define LIB_SO_SUFFIX_SIZE 9
#define LIB_PATH_MAX_SIZE 220

typedef struct {
    __u32 pid;
    __u32 len;
    char buf[LIB_PATH_MAX_SIZE];
} lib_path_t;

typedef struct {
    unsigned short common_type;
    unsigned char common_flags;
    unsigned char common_preempt_count;
    int common_pid;
    long __syscall_nr;

    const char* filename;
    int flags;
    int mode;
} enter_sys_open_ctx;

typedef struct {
    unsigned short common_type;
    unsigned char common_flags;
    unsigned char common_preempt_count;
    int common_pid;
    long __syscall_nr;

    int dfd;
    const char* filename;
    int flags;
    int mode;
} enter_sys_openat_ctx;

typedef struct {
    __u64 flags;
    __u64 mode;
    __u64 resolve;
} openat2_open_how;

typedef struct {
    unsigned short common_type;
    unsigned char common_flags;
    unsigned char common_preempt_count;
    int common_pid;
    long __syscall_nr;

    int dfd;
    const char* filename;
    openat2_open_how *how;
    size_t usize;
} enter_sys_openat2_ctx;

typedef struct {
    unsigned short common_type;
    unsigned char common_flags;
    unsigned char common_preempt_count;
    int common_pid;
    int __syscall_nr;

    long ret;
} exit_sys_ctx;

#endif
