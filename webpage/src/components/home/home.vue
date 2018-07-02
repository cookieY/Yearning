<style lang="less">
  @import "./home.less";
  @import "../../styles/common.less";

  .fuc {
  }

  .fuc li {
    margin-top: 2%;
    margin-left: 15%;
  }

  .fuc h4 {
    margin-top: 2%;
    margin-left: 10%;
  }

  .fuc h3 {
  }
</style>
<template>
  <div class="home-main">
    <Row>
      <Col span="8">
        <Row>
          <Card>
            <Row type="flex" class="user-infor">
              <Col span="8">
                <Row class-name="made-child-con-middle" type="flex" align="middle">
                  <img class="avator-img" src="../../assets/avatar.png"/>
                </Row>
              </Col>
              <Col span="16" style="padding-left:6px;">
                <Row class-name="made-child-con-middle" type="flex" align="middle">
                  <div>
                    <b class="card-user-infor-name">{{username}}</b>
                    <p>Go confidently in the direction.</p>
                  </div>
                </Row>
              </Col>
            </Row>
            <div class="line-gray"></div>
            <Row class="margin-top-8">
              <Col span="8">
                <p class="notwrap">登陆时间:</p>
              </Col>
              <Col span="16" class="padding-left-8">{{time}}</Col>
            </Row>
          </Card>
        </Row>
        <Row class="margin-top-10">
          <Card>
            <p slot="title" class="card-title">
              <Icon type="android-checkbox-outline"></Icon>
              待办事项
            </p>
            <a type="text" slot="extra" @click.prevent="addNewToDoItem">
              <Icon type="plus-round"></Icon>
            </a>
            <Modal v-model="showAddNewTodo" title="添加新的待办事项" @on-ok="addNew" @on-cancel="cancelAdd">
              <Row type="flex" justify="center">
                <Input v-model="newToDoItemValue" icon="compose" placeholder="请输入..." style="width: 300px"/>
              </Row>
            </Modal>
            <div class="to-do-list-con">
              <div v-for="(item, index) in toDoList" :key="index" class="to-do-item">
                <to-do-list-item :content="item.title" :todoitem="false" @deltodo="deltodo"></to-do-list-item>
              </div>
            </div>
          </Card>
        </Row>
      </Col>
      <Col span="16" class-name="padding-left-5">
        <Row>
          <Col span="6">
            <infor-card id-name="user_created_count" :end-val="count.createUser" iconType="android-person-add"
                        color="#2d8cf0" intro-text="平台用户"></infor-card>
          </Col>
          <Col span="6" class-name="padding-left-5">
            <infor-card id-name="visit_count" :end-val="count.link" iconType="ios-eye" color="#64d572" :iconSize="50"
                        intro-text="数据库连接地址"></infor-card>
          </Col>
          <Col span="6" class-name="padding-left-5">
            <infor-card id-name="collection_count" :end-val="count.dic" iconType="upload" color="#ffd572"
                        intro-text="数据字典采集字段"></infor-card>
          </Col>
          <Col span="6" class-name="padding-left-5">
            <infor-card id-name="transfer_count" :end-val="count.order" iconType="shuffle" color="#f25e43"
                        intro-text="我提交的工单"></infor-card>
          </Col>
        </Row>
        <Row class="margin-top-10">
          <Col span="12">
            <Card>
              <p slot="title" class="card-title">
                <Icon type="android-map"></Icon>
                公告栏
              </p>
              <div class="data-sourcefunc-row">
                <H2>欢迎使用Yearning SQL 审核平台</H2>
                <br>
                <div class="fuc">
                  <H3>主要功能:</H3>
                  <H4 v-for="i in board.title" :key="i">{{i}}</H4>
                </div>
              </div>
            </Card>
          </Col>
          <Col span="12" class="padding-left-10">
            <Card>
              <p slot="title" class="card-title">
                <Icon type="ios-pulse-strong"></Icon>
                DDL & DML 工单提交统计
              </p>
              <div class="data-source-row">
                <data-source-pie></data-source-pie>
              </div>
            </Card>
          </Col>
        </Row>
      </Col>
    </Row>
  </div>
</template>

<script>
  import axios from 'axios'
  //
  import util from '../../libs/util'
  import dataSourcePie from './components/dataSourcePie.vue'
  import inforCard from './components/inforCard.vue'
  import toDoListItem from './components/toDoListItem.vue'

  export default {
    components: {
      dataSourcePie,
      inforCard,
      toDoListItem
    },
    data () {
      return {
        toDoList: [{
          title: ''
        }],
        count: {
          createUser: 0,
          order: 0,
          link: 0,
          dic: 0
        },
        showAddNewTodo: false,
        newToDoItemValue: '',
        time: '',
        username: sessionStorage.getItem('user'),
        board: {
          'title': ['1.DDL语句生成', '2.数据库字典生成及查看', '3.SQL语句审核及回滚', '4.工单流程化', '5.可视化数据查询', '6.细粒度的权限划分']
        }
      }
    },
    methods: {
      addNewToDoItem () {
        this.showAddNewTodo = true
      },
      formatDate () {
        let date = new Date()
        let year = date.getFullYear()
        let month = date.getMonth() + 1
        let day = date.getDate()
        let hour = date.getHours()
        let minute = date.getMinutes()
        let second = date.getSeconds()
        this.time = year + '/' + month + '/' + day + '  ' + hour + ':' + minute + ':' + second
      },
      addNew () {
        if (this.newToDoItemValue.length !== 0) {
          axios.post(`${util.url}/homedata/todolist/`, {
            'todo': this.newToDoItemValue
          })
            .then(() => {
              let vm = this
              this.toDoList.unshift({
                title: this.newToDoItemValue
              })
              setTimeout(function () {
                vm.newToDoItemValue = ''
              }, 200)
              this.showAddNewTodo = false
            })
            .catch(error => {
              util.err_notice(error)
            })
        } else {
          this.$Message.error('请输入待办事项内容')
        }
      },
      cancelAdd () {
        this.showAddNewTodo = false
        this.newToDoItemValue = ''
      },
      deltodo (val) {
        axios.put(`${util.url}/homedata/deltodo`, {
          'todo': val
        })
          .then(() => {
            this.gettodo()
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      gettodo () {
        axios.put(`${util.url}/homedata/todolist`)
          .then(res => {
            this.toDoList = res.data
          })
          .catch(error => {
            util.err_notice(error)
          })
      }
    },
    mounted () {
      axios.get(`${util.url}/homedata/infocard`)
        .then(res => {
          this.count.dic = res.data[0]
          this.count.createUser = res.data[1]
          this.count.order = res.data[2]
          this.count.link = res.data[3]
        })
        .catch(error => {
          util.err_notice(error)
        })
      this.gettodo()
      this.formatDate()
    }
  }
</script>
