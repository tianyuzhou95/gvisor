#include <unistd.h>
#include <sys/syscall.h>
#include <stdio.h>
#define SYS_OVERFLOW 338

struct A {
	unsigned int ui;
	int i;
	char c;
};

int main() {
	// printf("size of unsigned int: %ld\n", sizeof(unsigned int));
	struct A a;
	a.ui = 1;
	a.i = -1;
	a.c = 'a';
	struct A* ptr_a = &a;
	unsigned int temp;
	temp = *((unsigned int*)ptr_a);
	printf("%08x\n", temp);
	temp = *((unsigned int*)ptr_a+1);
	printf("%08x\n", temp);
	temp = *((char*)((unsigned int*)ptr_a+2));
	printf("%02d\n", temp);
	syscall(SYS_OVERFLOW, 0, 0);
	temp = *((unsigned int*)ptr_a);
	printf("%08x\n", temp);
	temp = *((unsigned int*)ptr_a+1);
	printf("%08x\n", temp);
	temp = *((char*)((unsigned int*)ptr_a+2));
	printf("%02d\n", temp);
	return 0;
}
