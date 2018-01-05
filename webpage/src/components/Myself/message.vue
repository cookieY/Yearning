<style lang="less">
@import './message.less';
</style>

<template>
<div class="message-main-con">
  <div class="message-mainlist-con">
    <div>
      <Button @click="setCurrentMesType('unread')" size="large" long type="text"><transition name="mes-current-type-btn"><Icon v-show="currentMessageType === 'unread'" type="checkmark"></Icon></transition><span class="mes-type-btn-text">未读消息</span><Badge class="message-count-badge-outer" class-name="message-count-badge" :count="unreadCount"></Badge></Button>
    </div>
    <div>
      <Button @click="setCurrentMesType('hasread')" size="large" long type="text"><transition name="mes-current-type-btn"><Icon v-show="currentMessageType === 'hasread'" type="checkmark"></Icon></transition><span class="mes-type-btn-text">已读消息</span><Badge class="message-count-badge-outer" class-name="message-count-badge" :count="hasreadCount"></Badge></Button>
    </div>
    <div>
      <Button @click="setCurrentMesType('recyclebin')" size="large" long type="text"><transition name="mes-current-type-btn"><Icon v-show="currentMessageType === 'recyclebin'" type="checkmark"></Icon></transition><span class="mes-type-btn-text">回收站</span><Badge class="message-count-badge-outer" class-name="message-count-badge" :count="recyclebinCount"></Badge></Button>
    </div>
  </div>
  <div class="message-content-con">
    <transition name="view-message">
      <div v-if="showMesTitleList" class="message-title-list-con">
        <Table ref="messageList" :columns="mesTitleColumns" :data="currentMesList" :no-data-text="noDataText"></Table>
      </div>
    </transition>
    <transition name="back-message-list">
      <div v-if="!showMesTitleList" class="message-view-content-con">
        <div class="message-content-top-bar">
          <span class="mes-back-btn-con"><Button type="text" @click="backMesTitleList"><Icon type="chevron-left"></Icon>&nbsp;&nbsp;返回</Button></span>
          <h3 class="mes-title">{{ mes.title }}</h3>
        </div>
        <p class="mes-time-con">
          <Icon type="android-time"></Icon>&nbsp;&nbsp;{{ `${mes.time}&nbsp&nbsp&nbsp审核人: &nbsp${mes.from_user}` }}</p>
        <div class="message-content-body">
          <p class="message-content" style="font-size: 16px">{{ mes.content }}</p>
        </div>
      </div>
    </transition>
  </div>
</div>
</template>

<script>
import axios from 'axios'
import Cookies from 'js-cookie'
import util from '../../libs/util'
export default {
  data () {
    const markAsreadBtn = (h, params) => {
      return h('Button', {
        props: {
          size: 'small'
        },
        on: {
          click: () => {
            this.hasreadMesList.unshift(this.currentMesList.splice(params.index, 1)[0]);
            this.$store.state.messageCount = this.$store.state.messageCount - 1
            // 更新为后端已读
            this.updateread(params.row.title, params.row.time)
          }
        }
      }, '标为已读');
    };
    const deleteMesBtn = (h, params) => {
      return h('Button', {
        props: {
          size: 'small',
          type: 'error'
        },
        on: {
          click: () => {
            this.recyclebinList.unshift(this.hasreadMesList.splice(params.index, 1)[0]);
            // 后端更新为删除
            this.deleteread(params.row.title, params.row.time)
          }
        }
      }, '删除');
    };
    const restoreBtn = (h, params) => {
      return h('Button', {
        props: {
          size: 'small'
        },
        on: {
          click: () => {
            this.hasreadMesList.unshift(this.recyclebinList.splice(params.index, 1)[0]);
            this.updateread(params.row.title, params.row.time)
          }
        }
      }, '还原'); // 后端更新为已读
    };
    return {
      currentMesList: [],
      unreadMesList: [],
      hasreadMesList: [],
      recyclebinList: [],
      currentMessageType: 'unread',
      showMesTitleList: true,
      unreadCount: 0,
      hasreadCount: 0,
      recyclebinCount: 0,
      noDataText: '暂无未读消息',
      mes: {
        title: '',
        time: '',
        content: '',
        from_user: ''
      },
      mesTitleColumns: [
        //                 {
        //                     type: 'selection',
        //                     width: 50,
        //                     align: 'center'
        //                 },
        {
          title: ' ',
          key: 'title',
          align: 'left',
          ellipsis: true,
          render: (h, params) => {
            return h('a', {
              on: {
                click: () => {
                  this.showMesTitleList = false;
                  this.mes.title = params.row.title;
                  this.mes.time = params.row.time;
                  this.getContent(params.row.title, params.row.time);
                }
              }
            }, params.row.title);
          }
        },
        {
          title: ' ',
          key: 'time',
          align: 'center',
          width: 180,
          render: (h, params) => {
            return h('span', [
              h('Icon', {
                props: {
                  type: 'android-time',
                  size: 12
                },
                style: {
                  margin: '0 5px'
                }
              }),
              h('span', {
                props: {
                  type: 'android-time',
                  size: 12
                }
              }, params.row.time)
            ]);
          }
        },
        {
          title: ' ',
          key: 'asread',
          align: 'center',
          width: 100,
          render: (h, params) => {
            if (this.currentMessageType === 'unread') {
              return h('div', [
                markAsreadBtn(h, params)
              ]);
            } else if (this.currentMessageType === 'hasread') {
              return h('div', [
                deleteMesBtn(h, params)
              ]);
            } else {
              return h('div', [
                restoreBtn(h, params)
              ]);
            }
          }
        }
      ]
    };
  },
  methods: {
    backMesTitleList () {
      this.showMesTitleList = true;
    },
    setCurrentMesType (type) {
      if (this.currentMessageType !== type) {
        this.showMesTitleList = true;
      }
      this.currentMessageType = type;
      if (type === 'unread') {
        this.noDataText = '暂无未读消息';
        this.currentMesList = this.unreadMesList;
      } else if (type === 'hasread') {
        this.noDataText = '暂无已读消息';
        this.currentMesList = this.hasreadMesList;
      } else {
        this.noDataText = '回收站无消息';
        this.currentMesList = this.recyclebinList;
      }
    },
    getContent (title, time) {
      axios.put(`${util.url}/messages/${Cookies.get('user')}`, {
          'title': title,
          'time': time
        })
        .then(res => {
          this.mes.content = res.data.content
          this.mes.from_user = res.data.from_user
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    },
    updateread (title, time) {
      axios.post(`${util.url}/messages/${Cookies.get('user')}/`, {
          'title': title,
          'time': time,
          'state': 'read'
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    },
    deleteread (title, time) {
      axios.delete(`${util.url}/messages/${Cookies.get('user')}_${title}_${time}`)
    }
  },
  mounted () {
    axios.get(`${util.url}/messages/${Cookies.get('user')}`)
      .then(res => {
        this.unreadMesList = res.data.unread
        this.hasreadMesList = res.data.read
        this.recyclebinList = res.data.recovery
        this.unreadCount = res.data.unread.length
        this.hasreadCount = res.data.read.length;
        this.recyclebinCount = res.data.recovery.length;
        this.currentMesList = this.unreadMesList
      })
      .catch(error => {
        util.ajanxerrorcode(this, error)
      })
  },
  watch: {
    unreadMesList (arr) {
      this.unreadCount = arr.length;
    },
    hasreadMesList (arr) {
      this.hasreadCount = arr.length;
    },
    recyclebinList (arr) {
      this.recyclebinCount = arr.length;
    }
  }
};
</script>
