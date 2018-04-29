/* Copyright (c) 2000, 2010, Oracle and/or its affiliates. All rights reserved.

   This program is free software; you can redistribute it and/or modify
   it under the terms of the GNU General Public License as published by
   the Free Software Foundation; version 2 of the License.

   This program is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU General Public License for more details.

   You should have received a copy of the GNU General Public License
   along with this program; if not, write to the Free Software
   Foundation, Inc., 51 Franklin St, Fifth Floor, Boston, MA 02110-1301  USA */

#ifndef _list_h_
#define _list_h_

#ifdef	__cplusplus
extern "C" {
#endif

typedef struct st_list {
  struct st_list *prev,*next;
  void *data;
} LIST;

typedef int (*list_walk_action)(void *,void *);

extern LIST *list_add(LIST *root,LIST *element);
extern LIST *list_delete(LIST *root,LIST *element);
extern LIST *list_cons(void *data,LIST *root);
extern LIST *list_reverse(LIST *root);
extern void list_free(LIST *root,unsigned int free_data);
extern unsigned int list_length(LIST *);
extern int list_walk(LIST *,list_walk_action action,unsigned char * argument);

#define list_rest(a) ((a)->next)
#define list_push(a,b) (a)=list_cons((b),(a))
#define list_pop(A) {LIST *old=(A); (A)=list_delete(old,old); my_free(old); }

#define LIST_BASE_NODE_T(TYPE)\
struct {\
	uint	count;	/* count of nodes in list */\
	TYPE *	start;	/* pointer to list start, NULL if empty */\
	TYPE *	end;	/* pointer to list end, NULL if empty */\
}\

#define LIST_NODE_T(TYPE)\
struct {\
	TYPE *	prev;\
	TYPE *	next;\
}\

#define LIST_INIT(BASE)\
do{\
	(BASE).count = 0;\
	(BASE).start = NULL;\
	(BASE).end   = NULL;\
}while(0)

#define LIST_ADD_FIRST(NAME, BASE, N)\
do{\
	((BASE).count)++;\
	((N)->NAME).next = (BASE).start;\
	((N)->NAME).prev = NULL;\
	if ((BASE).start != NULL) {\
		(((BASE).start)->NAME).prev = (N);\
	}\
	(BASE).start = (N);\
	if ((BASE).end == NULL) {\
		(BASE).end = (N);\
	}\
}while(0)

#define LIST_ADD_LAST(NAME, BASE, N)\
do{\
	((BASE).count)++;\
	((N)->NAME).prev = (BASE).end;\
	((N)->NAME).next = NULL;\
	if ((BASE).end != NULL) {\
		(((BASE).end)->NAME).next = (N);\
	}\
	(BASE).end = (N);\
	if ((BASE).start == NULL) {\
		(BASE).start = (N);\
	}\
}while(0)

#define LIST_DATA_APPEND(lst,d) \
do{\
    lst_node_t*  node;\
    node = (lst_node_t*)my_malloc(sizeof(lst_node_t), MYF(MY_WME));\
    node->data = (d);\
    LIST_ADD_LAST(link, (lst), node);\
}while(0)

#define LIST_DATA_FLAG_APPEND(lst,d,f,g) \
	do{\
	lst_node_t*  node;\
	node = (lst_node_t*)my_malloc(sizeof(lst_node_t), MYF(MY_WME));\
	node->data = (d);\
	node->flag = (f);\
	node->attr = (g);\
	LIST_ADD_LAST(link, (lst), node);\
}while(0)

#define LIST_DATA_ADD_FIRST(lst,d) \
do{\
    lst_node_t*  node;\
    node = (lst_node_t*)my_malloc(sizeof(lst_node_t), MYF(MY_WME));\
    node->data = (void*)(d);\
    LIST_ADD_FIRST(link, (lst), node);\
}while(0)

#define LIST_INSERT_BEFORE(NAME, BASE, NODE1, NODE2)\
do{\
	((BASE).count)++;\
	((NODE2)->NAME).next = (NODE1);\
	((NODE2)->NAME).prev = ((NODE1)->NAME).prev;\
	if (((NODE1)->NAME).prev != NULL) {\
		((((NODE1)->NAME).prev)->NAME).next = (NODE2);\
	}\
	((NODE1)->NAME).prev = (NODE2);\
	if ((BASE).start == (NODE1)) {\
		(BASE).start = (NODE2);\
	}\
}while(0)

#define LIST_INSERT_AFTER(NAME, BASE, NODE1, NODE2)\
do{\
	((BASE).count)++;\
	((NODE2)->NAME).prev = (NODE1);\
	((NODE2)->NAME).next = ((NODE1)->NAME).next;\
	if (((NODE1)->NAME).next != NULL) {\
		((((NODE1)->NAME).next)->NAME).prev = (NODE2);\
	}\
	((NODE1)->NAME).next = (NODE2);\
	if ((BASE).end == (NODE1)) {\
		(BASE).end = (NODE2);\
	}\
}while(0)

#define LIST_REMOVE(NAME, BASE, N)\
do{\
	((BASE).count)--;\
	if (((N)->NAME).next != NULL) {\
		((((N)->NAME).next)->NAME).prev = ((N)->NAME).prev;\
	} else {\
		(BASE).end = ((N)->NAME).prev;\
	}\
	if (((N)->NAME).prev != NULL) {\
		((((N)->NAME).prev)->NAME).next = ((N)->NAME).next;\
	} else {\
		(BASE).start = ((N)->NAME).next;\
	}\
	((N)->NAME).next = NULL;\
	((N)->NAME).prev = NULL;\
}while(0)

#define LIST_GET_NEXT(NAME, N)\
	(((N)->NAME).next)

#define LIST_GET_PREV(NAME, N)\
	(((N)->NAME).prev)

#define LIST_GET_LEN(BASE)\
	(BASE).count

#define LIST_GET_FIRST(BASE)\
	(BASE).start

#define LIST_GET_LAST(BASE)\
	(BASE).end
typedef struct lst_node_struct lst_node_t;
struct lst_node_struct
{
	void*					data;
	void*					flag;
	LIST_NODE_T(lst_node_t) link;
};

typedef LIST_BASE_NODE_T(lst_node_t) my_lst_t;

#ifdef  __cplusplus
}
#endif
#endif
